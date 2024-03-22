package aurp

type ErrorCode = int16

// Various error codes.
const (
	ErrCodeNormalClose           = -1
	ErrCodeRoutingLoop           = -2
	ErrCodeOutOfSync             = -3
	ErrCodeOptionNegotiation     = -4
	ErrCodeInvalidVersion        = -5
	ErrCodeInsufficientResources = -6
	ErrCodeAuthentication        = -7
)
