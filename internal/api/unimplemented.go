// Code generated pattern (hand-maintained): one method per contract
// operation. When the spec adds an operation, compilation breaks here until
// it is implemented or consciously 501'd — that is the drift gate.
package api

import (
	"context"
	"errors"
)

// ErrNotImplemented marks a contract operation that has no implementation
// yet; the HTTP layer maps it to a 501 Problem.
var ErrNotImplemented = errors.New("not implemented")

// Unimplemented satisfies StrictServerInterface with 501s. Real servers
// embed it and override what they implement.
type Unimplemented struct{}

func (Unimplemented) AdminListDevices(ctx context.Context, request AdminListDevicesRequestObject) (AdminListDevicesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminRevokeDevice(ctx context.Context, request AdminRevokeDeviceRequestObject) (AdminRevokeDeviceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminDeleteLastfm(ctx context.Context, request AdminDeleteLastfmRequestObject) (AdminDeleteLastfmResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminGetLastfm(ctx context.Context, request AdminGetLastfmRequestObject) (AdminGetLastfmResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminSetLastfm(ctx context.Context, request AdminSetLastfmRequestObject) (AdminSetLastfmResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminDeleteLidarr(ctx context.Context, request AdminDeleteLidarrRequestObject) (AdminDeleteLidarrResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminGetLidarr(ctx context.Context, request AdminGetLidarrRequestObject) (AdminGetLidarrResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminSetLidarr(ctx context.Context, request AdminSetLidarrRequestObject) (AdminSetLidarrResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminTestLidarr(ctx context.Context, request AdminTestLidarrRequestObject) (AdminTestLidarrResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminCreatePairCode(ctx context.Context, request AdminCreatePairCodeRequestObject) (AdminCreatePairCodeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminListPairings(ctx context.Context, request AdminListPairingsRequestObject) (AdminListPairingsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminApprovePairing(ctx context.Context, request AdminApprovePairingRequestObject) (AdminApprovePairingResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminDenyPairing(ctx context.Context, request AdminDenyPairingRequestObject) (AdminDenyPairingResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminListProfiles(ctx context.Context, request AdminListProfilesRequestObject) (AdminListProfilesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminCreateProfile(ctx context.Context, request AdminCreateProfileRequestObject) (AdminCreateProfileResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminDeleteProfile(ctx context.Context, request AdminDeleteProfileRequestObject) (AdminDeleteProfileResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminUpdateProfile(ctx context.Context, request AdminUpdateProfileRequestObject) (AdminUpdateProfileResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminLogout(ctx context.Context, request AdminLogoutRequestObject) (AdminLogoutResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminLogin(ctx context.Context, request AdminLoginRequestObject) (AdminLoginResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminGetServerSettings(ctx context.Context, request AdminGetServerSettingsRequestObject) (AdminGetServerSettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminSetServerSettings(ctx context.Context, request AdminSetServerSettingsRequestObject) (AdminSetServerSettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminGetTranscodeSettings(ctx context.Context, request AdminGetTranscodeSettingsRequestObject) (AdminGetTranscodeSettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminSetTranscodeSettings(ctx context.Context, request AdminSetTranscodeSettingsRequestObject) (AdminSetTranscodeSettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminSetup(ctx context.Context, request AdminSetupRequestObject) (AdminSetupResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminListPlexLibraries(ctx context.Context, request AdminListPlexLibrariesRequestObject) (AdminListPlexLibrariesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminSelectPlexLibrary(ctx context.Context, request AdminSelectPlexLibraryRequestObject) (AdminSelectPlexLibraryResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminStartPlexPin(ctx context.Context, request AdminStartPlexPinRequestObject) (AdminStartPlexPinResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminGetPlexPin(ctx context.Context, request AdminGetPlexPinRequestObject) (AdminGetPlexPinResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminGetState(ctx context.Context, request AdminGetStateRequestObject) (AdminGetStateResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetAcquisitionActivity(ctx context.Context, request GetAcquisitionActivityRequestObject) (GetAcquisitionActivityResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListAlbums(ctx context.Context, request ListAlbumsRequestObject) (ListAlbumsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetAlbum(ctx context.Context, request GetAlbumRequestObject) (GetAlbumResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListAlbumTracks(ctx context.Context, request ListAlbumTracksRequestObject) (ListAlbumTracksResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetArt(ctx context.Context, request GetArtRequestObject) (GetArtResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) RequestArtifacts(ctx context.Context, request RequestArtifactsRequestObject) (RequestArtifactsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ReleaseArtifact(ctx context.Context, request ReleaseArtifactRequestObject) (ReleaseArtifactResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetArtifact(ctx context.Context, request GetArtifactRequestObject) (GetArtifactResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) DownloadArtifact(ctx context.Context, request DownloadArtifactRequestObject) (DownloadArtifactResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListArtists(ctx context.Context, request ListArtistsRequestObject) (ListArtistsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetArtist(ctx context.Context, request GetArtistRequestObject) (GetArtistResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListArtistAlbums(ctx context.Context, request ListArtistAlbumsRequestObject) (ListArtistAlbumsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListSimilarArtists(ctx context.Context, request ListSimilarArtistsRequestObject) (ListSimilarArtistsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListArtistTracks(ctx context.Context, request ListArtistTracksRequestObject) (ListArtistTracksResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetCapabilities(ctx context.Context, request GetCapabilitiesRequestObject) (GetCapabilitiesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) StreamEvents(ctx context.Context, request StreamEventsRequestObject) (StreamEventsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetExternalArtist(ctx context.Context, request GetExternalArtistRequestObject) (GetExternalArtistResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListGenres(ctx context.Context, request ListGenresRequestObject) (ListGenresResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListGenreTracks(ctx context.Context, request ListGenreTracksRequestObject) (ListGenreTracksResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetHome(ctx context.Context, request GetHomeRequestObject) (GetHomeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetLibrary(ctx context.Context, request GetLibraryRequestObject) (GetLibraryResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListLoves(ctx context.Context, request ListLovesRequestObject) (ListLovesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) SetLove(ctx context.Context, request SetLoveRequestObject) (SetLoveResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetMe(ctx context.Context, request GetMeRequestObject) (GetMeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetMyDiscover(ctx context.Context, request GetMyDiscoverRequestObject) (GetMyDiscoverResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) DisconnectMyLastfm(ctx context.Context, request DisconnectMyLastfmRequestObject) (DisconnectMyLastfmResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetMyLastfm(ctx context.Context, request GetMyLastfmRequestObject) (GetMyLastfmResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ConnectMyLastfm(ctx context.Context, request ConnectMyLastfmRequestObject) (ConnectMyLastfmResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetMySettings(ctx context.Context, request GetMySettingsRequestObject) (GetMySettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) UpdateMySettings(ctx context.Context, request UpdateMySettingsRequestObject) (UpdateMySettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListMixes(ctx context.Context, request ListMixesRequestObject) (ListMixesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListMixTracks(ctx context.Context, request ListMixTracksRequestObject) (ListMixTracksResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) StartPairing(ctx context.Context, request StartPairingRequestObject) (StartPairingResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ClaimPairCode(ctx context.Context, request ClaimPairCodeRequestObject) (ClaimPairCodeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetPairing(ctx context.Context, request GetPairingRequestObject) (GetPairingResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListParties(ctx context.Context, request ListPartiesRequestObject) (ListPartiesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) CreateParty(ctx context.Context, request CreatePartyRequestObject) (CreatePartyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) EndParty(ctx context.Context, request EndPartyRequestObject) (EndPartyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetParty(ctx context.Context, request GetPartyRequestObject) (GetPartyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) InviteToParty(ctx context.Context, request InviteToPartyRequestObject) (InviteToPartyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) JoinParty(ctx context.Context, request JoinPartyRequestObject) (JoinPartyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) KickFromParty(ctx context.Context, request KickFromPartyRequestObject) (KickFromPartyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) LeaveParty(ctx context.Context, request LeavePartyRequestObject) (LeavePartyResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AppendPartyQueue(ctx context.Context, request AppendPartyQueueRequestObject) (AppendPartyQueueResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) PartyTransport(ctx context.Context, request PartyTransportRequestObject) (PartyTransportResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetPing(ctx context.Context, request GetPingRequestObject) (GetPingResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ReportPlaybackEvents(ctx context.Context, request ReportPlaybackEventsRequestObject) (ReportPlaybackEventsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListPlaylists(ctx context.Context, request ListPlaylistsRequestObject) (ListPlaylistsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) CreatePlaylist(ctx context.Context, request CreatePlaylistRequestObject) (CreatePlaylistResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) DeletePlaylist(ctx context.Context, request DeletePlaylistRequestObject) (DeletePlaylistResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetPlaylist(ctx context.Context, request GetPlaylistRequestObject) (GetPlaylistResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) UpdatePlaylist(ctx context.Context, request UpdatePlaylistRequestObject) (UpdatePlaylistResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListPlaylistTracks(ctx context.Context, request ListPlaylistTracksRequestObject) (ListPlaylistTracksResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AppendPlaylistTracks(ctx context.Context, request AppendPlaylistTracksRequestObject) (AppendPlaylistTracksResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) RemovePlaylistTrack(ctx context.Context, request RemovePlaylistTrackRequestObject) (RemovePlaylistTrackResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetPresence(ctx context.Context, request GetPresenceRequestObject) (GetPresenceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) CreateProfileToken(ctx context.Context, request CreateProfileTokenRequestObject) (CreateProfileTokenResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListProfiles(ctx context.Context, request ListProfilesRequestObject) (ListProfilesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetRadioNext(ctx context.Context, request GetRadioNextRequestObject) (GetRadioNextResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) SetRating(ctx context.Context, request SetRatingRequestObject) (SetRatingResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListRecommendations(ctx context.Context, request ListRecommendationsRequestObject) (ListRecommendationsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) CreateRecommendation(ctx context.Context, request CreateRecommendationRequestObject) (CreateRecommendationResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) MarkRecommendationSeen(ctx context.Context, request MarkRecommendationSeenRequestObject) (MarkRecommendationSeenResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) Search(ctx context.Context, request SearchRequestObject) (SearchResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetStatus(ctx context.Context, request GetStatusRequestObject) (GetStatusResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) CreateStreamGrant(ctx context.Context, request CreateStreamGrantRequestObject) (CreateStreamGrantResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) StreamTrack(ctx context.Context, request StreamTrackRequestObject) (StreamTrackResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetTasteSnapshot(ctx context.Context, request GetTasteSnapshotRequestObject) (GetTasteSnapshotResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) ListTracks(ctx context.Context, request ListTracksRequestObject) (ListTracksResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) GetTrack(ctx context.Context, request GetTrackRequestObject) (GetTrackResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminDeleteFilesystemSource(ctx context.Context, request AdminDeleteFilesystemSourceRequestObject) (AdminDeleteFilesystemSourceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminGetFilesystemSource(ctx context.Context, request AdminGetFilesystemSourceRequestObject) (AdminGetFilesystemSourceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminSetFilesystemSource(ctx context.Context, request AdminSetFilesystemSourceRequestObject) (AdminSetFilesystemSourceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (Unimplemented) AdminScanFilesystemSource(ctx context.Context, request AdminScanFilesystemSourceRequestObject) (AdminScanFilesystemSourceResponseObject, error) {
	return nil, ErrNotImplemented
}
