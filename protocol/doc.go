// Package protocol defines Hostel's public Bed management data contract.
// Both the server and callers use these definitions; runtime resources and
// publication policies remain with their implementation owners.
//
// +rule=`Public protocol types depend only on the standard library, never on internal runtime packages`
// +why=`An independent module keeps callers free of the daemon toolchain and runtime dependencies`
// +link=component://docs/protocol.md
package protocol
