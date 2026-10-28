package stack

import "context"

// Only Operator actions use transport attribution; an Agent keeps its identity.
func operatorActor(ctx context.Context, p Principal) (any, any) {
	if p.Kind != OperatorCapability && p.Kind != BrowserCapability {
		return nil, nil
	}
	a, ok := ActorFromContext(ctx)
	if !ok {
		return nil, nil
	}
	return a.Login, a.Name
}

func operatorLabel(login, name string) string {
	if name != "" {
		return name
	}
	if login != "" {
		return "Operator (" + login + ")"
	}
	return "Operator"
}
