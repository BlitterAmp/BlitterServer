package api

import "fmt"

// StatusError lets a strict handler answer with a status the contract does
// not enumerate as a typed response (e.g. request validation 400s). The HTTP
// layer's response-error hook renders it as an RFC 9457 problem.
type StatusError struct {
	Status int
	Code   string
	Title  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s (%d)", e.Code, e.Status)
}
