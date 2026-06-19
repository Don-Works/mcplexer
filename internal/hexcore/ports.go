package hexcore

import "context"

type InputPort interface {
	Name() string
	Run(ctx context.Context, out chan<- Event) error
	Stop() error
}

type PairingInputPort interface {
	InputPort
	ConsumePairing(ctx context.Context, platform string, code string) (string, error)
}

type OutputPort interface {
	Name() string
	Deliver(ctx context.Context, action Action) error
	CanDeliver(action Action) bool
}

type EventHandler func(ctx context.Context, event Event) error

type EventRouter interface {
	Route(ctx context.Context, event Event) error
	RegisterHandler(kind string, handler EventHandler)
}

type ActionDispatcher interface {
	Dispatch(ctx context.Context, action Action) error
	RegisterOutput(port OutputPort)
}
