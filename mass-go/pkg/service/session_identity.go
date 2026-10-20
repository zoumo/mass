package service

import (
	"errors"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// ResolveSession maps an HTTP name-or-ID target to a fixed, exclusive ID
// reference. Operations never repeat name resolution after this boundary.
func (s *Service) ResolveSession(namespace, nameOrID string) (resource.SessionRef, error) {
	if err := resource.ValidateSessionID(nameOrID); err != nil {
		return resource.SessionRef{}, err
	}
	if !validNamespace(namespace) {
		return resource.SessionRef{}, runtime.InvalidArgument("invalid namespace")
	}
	byName, nameErr := s.meta.GetSession(resource.SessionRef{Namespace: namespace, Name: nameOrID})
	if nameErr != nil && !errors.Is(nameErr, store.ErrNotFound) {
		return resource.SessionRef{}, nameErr
	}
	byID, idErr := s.meta.GetSession(resource.SessionRef{SessionID: nameOrID})
	if idErr != nil && !errors.Is(idErr, store.ErrNotFound) {
		return resource.SessionRef{}, idErr
	}
	nameMatch := nameErr == nil
	idMatch := idErr == nil && byID.Metadata.Namespace == namespace
	if nameMatch && idMatch && byName.Status.SessionID != byID.Status.SessionID {
		return resource.SessionRef{}, store.ErrConflict
	}
	if nameMatch {
		if err := resource.ValidateSessionID(byName.Status.SessionID); err != nil {
			return resource.SessionRef{}, err
		}
		return resource.SessionRef{SessionID: byName.Status.SessionID}, nil
	}
	if idMatch {
		return resource.SessionRef{SessionID: byID.Status.SessionID}, nil
	}
	return resource.SessionRef{}, store.ErrNotFound
}
