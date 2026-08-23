// Package llrp provides bounded, protocol-only LLRP message and parameter
// encoding and decoding.
//
// Message decoding validates frame lengths before allocation and copies
// decoded payloads into Message ownership. Limits control frame size,
// parameter size, and parameter count for untrusted input.
//
// Network lifecycle, dialing, deadlines, retries, logging, and connection
// ownership remain with callers. Use ReadMessage and WriteMessage at transport
// boundaries, and use DecodeMessage or DecodeParameters for in-memory data.
package llrp
