// Package llrp provides bounded, protocol-only LLRP message and parameter
// encoding and decoding.
//
// The package encodes and decodes and does nothing else: network lifecycle,
// dialing, deadlines, retries, logging, and connection ownership remain with
// callers. Use ReadMessage and WriteMessage at transport boundaries, and use
// DecodeMessage or DecodeParameters for in-memory data. ReadMessage never owns
// the reader, so set deadlines on the connection yourself.
//
// # Bounds
//
// Limits bounds the work done on untrusted input, and zero fields select the
// defaults: MaxFrameSize 1 MiB, MaxParameterSize 32 KiB, and MaxParameters
// 4,096 per parameter list. Frame lengths are validated before anything is
// allocated.
//
// # Ownership
//
// Message.Payload and every Parameter.Data returned by DecodeParameters are
// copies the caller owns. DecodeReadEvents validates each parameter list in
// full before inspecting it, inspects nested parameters in place, and copies
// each event's EPC and PC once, so events never alias the input. It accepts
// the EPC in either EPCData form (TLV 241 or TV 13) and skips tag reports
// without an EPC. DecodeParameters preserves unknown TLV parameters and
// rejects unknown TV parameters, because their length is implicit.
//
// # Errors
//
// Errors wrap the package's Err values, such as ErrFrameTooSmall; match them
// with errors.Is.
package llrp
