package probe

import (
	"errors"

	"github.com/Resinat/Resin/internal/outbound"
)

func isSkippedProbe(err error) bool {
	return errors.Is(err, outbound.ErrSkipProbe)
}
