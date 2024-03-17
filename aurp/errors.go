package aurp

type ErrorCode int16

// Various error codes.
const (
	ErrCodeNormalClose           ErrorCode = -1
	ErrCodeRoutingLoop           ErrorCode = -2
	ErrCodeOutOfSync             ErrorCode = -3
	ErrCodeOptionNegotiation     ErrorCode = -4
	ErrCodeInvalidVersion        ErrorCode = -5
	ErrCodeInsufficientResources ErrorCode = -6
	ErrCodeAuthentication        ErrorCode = -7
)
