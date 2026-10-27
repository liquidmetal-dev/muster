package battery

import (
	"context"
	"errors"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// Events implements vmsource.Source. Task 6 fills it in.
func (s *Source) Events(context.Context, string) (<-chan vmsource.Event, error) {
	return nil, errors.New("battery: Events is not implemented yet")
}
