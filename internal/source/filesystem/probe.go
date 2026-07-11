package filesystem

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MediaProbe is what the library index needs to know about an audio file's
// encoding, derived natively (no ffprobe dependency).
type MediaProbe struct {
	Container   string
	Codec       string
	DurationMs  int
	BitrateKbps int
	SizeBytes   int64
}

// Probe inspects an audio file by extension + native header parsing.
func Probe(path string) (MediaProbe, error) {
	f, err := os.Open(path)
	if err != nil {
		return MediaProbe{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return MediaProbe{}, err
	}

	var p MediaProbe
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		p, err = probeFLAC(f)
	case ".mp3":
		p, err = probeMP3(f, st.Size())
	case ".m4a", ".mp4":
		p, err = probeMP4(f)
	case ".ogg", ".opus", ".oga":
		p, err = probeOgg(f, st.Size())
	default:
		return MediaProbe{}, fmt.Errorf("probe %s: unsupported extension", filepath.Base(path))
	}
	if err != nil {
		return MediaProbe{}, fmt.Errorf("probe %s: %w", filepath.Base(path), err)
	}
	p.SizeBytes = st.Size()
	if p.BitrateKbps == 0 && p.DurationMs > 0 {
		p.BitrateKbps = int(st.Size() * 8 / int64(p.DurationMs)) // bytes*8 / ms = kbit/s
	}
	return p, nil
}

// ── FLAC: STREAMINFO block ─────────────────────────────────────

func probeFLAC(f *os.File) (MediaProbe, error) {
	head := make([]byte, 4+4+34) // magic + block header + STREAMINFO
	if _, err := io.ReadFull(f, head); err != nil {
		return MediaProbe{}, err
	}
	if string(head[:4]) != "fLaC" {
		return MediaProbe{}, fmt.Errorf("not a flac stream")
	}
	if head[4]&0x7f != 0 { // first block must be STREAMINFO (type 0)
		return MediaProbe{}, fmt.Errorf("missing STREAMINFO")
	}
	si := head[8:]
	// Layout from byte 10: 20 bits sample rate, 3 bits channels-1,
	// 5 bits bps-1, 36 bits total samples.
	sampleRate := int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
	totalSamples := (uint64(si[13]&0x0f) << 32) | uint64(binary.BigEndian.Uint32(si[14:18]))
	if sampleRate == 0 {
		return MediaProbe{}, fmt.Errorf("flac reports zero sample rate")
	}
	return MediaProbe{
		Container:  "flac",
		Codec:      "flac",
		DurationMs: int(totalSamples * 1000 / uint64(sampleRate)),
	}, nil
}

// ── MP3: Xing/Info frame count, else CBR estimate ─────────────

var mp3Bitrates = map[int][15]int{ // kbps by version group, layer III
	1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}, // MPEG1
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},     // MPEG2/2.5
}

var mp3Rates = map[int][3]int{ // by version bits
	3: {44100, 48000, 32000}, // MPEG1
	2: {22050, 24000, 16000}, // MPEG2
	0: {11025, 12000, 8000},  // MPEG2.5
}

func probeMP3(f *os.File, size int64) (MediaProbe, error) {
	buf := make([]byte, 64*1024)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return MediaProbe{}, err
	}
	buf = buf[:n]

	audioStart := int64(0)
	if len(buf) > 10 && string(buf[:3]) == "ID3" {
		tagSize := int64(buf[6])<<21 | int64(buf[7])<<14 | int64(buf[8])<<7 | int64(buf[9])
		audioStart = 10 + tagSize
		if audioStart < int64(len(buf)) {
			buf = buf[audioStart:]
		} else {
			buf = make([]byte, 8*1024)
			n, err := f.ReadAt(buf, audioStart)
			if err != nil && err != io.EOF {
				return MediaProbe{}, err
			}
			buf = buf[:n]
		}
	}

	// Find the first valid layer-III frame header.
	for i := 0; i+40 < len(buf); i++ {
		if buf[i] != 0xff || buf[i+1]&0xe0 != 0xe0 {
			continue
		}
		version := int(buf[i+1] >> 3 & 0x03) // 3=MPEG1 2=MPEG2 0=MPEG2.5
		layer := int(buf[i+1] >> 1 & 0x03)   // 1 = layer III
		if version == 1 || layer != 1 {
			continue
		}
		brIdx := int(buf[i+2] >> 4)
		srIdx := int(buf[i+2] >> 2 & 0x03)
		if brIdx == 0 || brIdx == 15 || srIdx == 3 {
			continue
		}
		group := 1
		samplesPerFrame := 1152
		if version != 3 {
			group = 2
			samplesPerFrame = 576
		}
		bitrate := mp3Bitrates[group][brIdx]
		sampleRate := mp3Rates[version][srIdx]

		// Xing/Info header for VBR frame counts.
		sideInfo := 32
		if version == 3 { // MPEG1
			if buf[i+3]>>6 == 3 { // mono
				sideInfo = 17
			}
		} else {
			sideInfo = 17
			if buf[i+3]>>6 == 3 {
				sideInfo = 9
			}
		}
		xingOff := i + 4 + sideInfo
		if xingOff+16 < len(buf) {
			tag := string(buf[xingOff : xingOff+4])
			if tag == "Xing" || tag == "Info" {
				flags := binary.BigEndian.Uint32(buf[xingOff+4 : xingOff+8])
				if flags&0x1 != 0 {
					frames := binary.BigEndian.Uint32(buf[xingOff+8 : xingOff+12])
					durMs := int(uint64(frames) * uint64(samplesPerFrame) * 1000 / uint64(sampleRate))
					kbps := 0
					if durMs > 0 {
						kbps = int((size - audioStart) * 8 / int64(durMs))
					}
					return MediaProbe{Container: "mp3", Codec: "mp3", DurationMs: durMs, BitrateKbps: kbps}, nil
				}
			}
		}

		// CBR estimate from the first frame's bitrate.
		durMs := int((size - audioStart - int64(i)) * 8 / int64(bitrate))
		return MediaProbe{Container: "mp3", Codec: "mp3", DurationMs: durMs, BitrateKbps: bitrate}, nil
	}
	return MediaProbe{}, fmt.Errorf("no mp3 frame header found")
}

// ── MP4/M4A: moov/mvhd + stsd codec ───────────────────────────

func probeMP4(f *os.File) (MediaProbe, error) {
	st, err := f.Stat()
	if err != nil {
		return MediaProbe{}, err
	}
	var durMs int
	codec := "aac"
	found := false

	var walk func(start, end int64, depth int) error
	walk = func(start, end int64, depth int) error {
		if depth > 8 {
			return nil
		}
		off := start
		hdr := make([]byte, 8)
		for off+8 <= end {
			if _, err := f.ReadAt(hdr, off); err != nil {
				return err
			}
			boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
			boxType := string(hdr[4:8])
			headerLen := int64(8)
			if boxSize == 1 { // 64-bit size
				big := make([]byte, 8)
				if _, err := f.ReadAt(big, off+8); err != nil {
					return err
				}
				boxSize = int64(binary.BigEndian.Uint64(big))
				headerLen = 16
			} else if boxSize == 0 {
				boxSize = end - off
			}
			if boxSize < headerLen {
				return fmt.Errorf("corrupt box %q", boxType)
			}
			switch boxType {
			case "moov", "trak", "mdia", "minf", "stbl":
				if err := walk(off+headerLen, off+boxSize, depth+1); err != nil {
					return err
				}
			case "mvhd":
				b := make([]byte, 32)
				if _, err := f.ReadAt(b, off+headerLen); err != nil {
					return err
				}
				if b[0] == 1 { // version 1
					timescale := binary.BigEndian.Uint32(b[20:24])
					duration := binary.BigEndian.Uint64(b[24:32])
					if timescale > 0 {
						durMs = int(duration * 1000 / uint64(timescale))
					}
				} else {
					timescale := binary.BigEndian.Uint32(b[12:16])
					duration := binary.BigEndian.Uint32(b[16:20])
					if timescale > 0 {
						durMs = int(uint64(duration) * 1000 / uint64(timescale))
					}
				}
				found = true
			case "stsd":
				b := make([]byte, 16)
				if _, err := f.ReadAt(b, off+headerLen); err == nil {
					fourcc := string(b[12:16])
					switch fourcc {
					case "alac":
						codec = "alac"
					case "mp4a":
						codec = "aac"
					}
				}
			}
			off += boxSize
		}
		return nil
	}
	if err := walk(0, st.Size(), 0); err != nil {
		return MediaProbe{}, err
	}
	if !found {
		return MediaProbe{}, fmt.Errorf("no mvhd box")
	}
	return MediaProbe{Container: "m4a", Codec: codec, DurationMs: durMs}, nil
}

// ── OGG (vorbis/opus): id header + last-page granule ──────────

func probeOgg(f *os.File, size int64) (MediaProbe, error) {
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return MediaProbe{}, err
	}
	head = head[:n]
	if len(head) < 58 || string(head[:4]) != "OggS" {
		return MediaProbe{}, fmt.Errorf("not an ogg stream")
	}

	codec := ""
	sampleRate := 0
	preSkip := 0
	// The first packet starts right after the first page header
	// (27 bytes + segment table).
	segs := int(head[26])
	packet := head[27+segs:]
	switch {
	case len(packet) > 19 && string(packet[:8]) == "OpusHead":
		codec = "opus"
		sampleRate = 48000 // opus output timebase is always 48kHz
		preSkip = int(binary.LittleEndian.Uint16(packet[10:12]))
	case len(packet) > 16 && string(packet[1:7]) == "vorbis":
		codec = "vorbis"
		sampleRate = int(binary.LittleEndian.Uint32(packet[12:16]))
	default:
		return MediaProbe{}, fmt.Errorf("unrecognized ogg codec")
	}
	if sampleRate == 0 {
		return MediaProbe{}, fmt.Errorf("ogg reports zero sample rate")
	}

	// Scan the tail for the last page's granule position.
	tailLen := int64(64 * 1024)
	if tailLen > size {
		tailLen = size
	}
	tail := make([]byte, tailLen)
	if _, err := f.ReadAt(tail, size-tailLen); err != nil && err != io.EOF {
		return MediaProbe{}, err
	}
	last := -1
	for i := 0; i+14 <= len(tail); i++ {
		if tail[i] == 'O' && tail[i+1] == 'g' && tail[i+2] == 'g' && tail[i+3] == 'S' {
			last = i
		}
	}
	if last < 0 {
		return MediaProbe{}, fmt.Errorf("no final ogg page")
	}
	granule := binary.LittleEndian.Uint64(tail[last+6 : last+14])
	samples := int64(granule) - int64(preSkip)
	if samples < 0 {
		samples = 0
	}
	container := "ogg"
	if codec == "opus" {
		container = "opus"
	}
	return MediaProbe{
		Container:  container,
		Codec:      codec,
		DurationMs: int(samples * 1000 / int64(sampleRate)),
	}, nil
}
