package isolator

type Isolator interface {
	Base
	DNS() DNSDomain
	Route() HostRoute
}
