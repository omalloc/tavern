//go:generate go run gen.go
//go:generate go fmt protocol_generated.go

package protocol

type Protocol string

const AppName = "tavern"

// flag constants
const (
	FlagOn  = "1" // gateway control flag ON
	FlagOff = "0" // gateway control flag OFF
)
