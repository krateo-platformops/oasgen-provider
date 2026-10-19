package apiaction

type APIAction string

const (
	Create APIAction = "create"
	Update APIAction = "update"
	Delete APIAction = "delete"
	List   APIAction = "list"
	Get    APIAction = "get"
	FindBy APIAction = "findby"
	// Dispatch is the non-CRUD fire-and-forget verb (#199). It is the resource's create -- a dispatch
	// kind declares no create verb at all -- and it is observed through its correlation collection
	// rather than through a get or findby.
	Dispatch APIAction = "dispatch"
)

func (a APIAction) String() string {
	return string(a)
}
