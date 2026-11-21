package core

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

type RuntimeBindingRelease struct {
	SessionBindingID string       `json:"sessionBindingId"`
	SessionID        string       `json:"sessionId"`
	SessionUID       string       `json:"sessionUid"`
	Close            BindingClose `json:"close"`
}

// A shared Runtime stop can account for another retired binding only if Core
// recorded the same Runtime instance and massd verifies that binding offline.
func (s *Server) observeRuntimeReleases(ctx context.Context, c *client.Client, rt resource.Runtime, targetID string) []RuntimeBindingRelease {
	type candidate struct {
		binding SessionBinding
		spec    resource.SessionSpec
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT body,spec FROM session_bindings WHERE state='revoked' AND id<>?
 AND json_extract(body,'$.runtimeUid')=? AND json_extract(body,'$.runtimeResourceVersion')=?
 AND json_type(body,'$.close') IS NULL AND NOT COALESCE(json_extract(body,'$.previousOwnerUnknown'),0)`,
		targetID, rt.Metadata.UID, rt.Metadata.ResourceVersion)
	if err != nil {
		return nil
	}
	var candidates []candidate
	for rows.Next() {
		var entry candidate
		var b, spec []byte
		if rows.Scan(&b, &spec) != nil || json.Unmarshal(b, &entry.binding) != nil ||
			json.Unmarshal(spec, &entry.spec) != nil {
			_ = rows.Close()
			return nil
		}
		candidates = append(candidates, entry)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return nil
	}
	var releases []RuntimeBindingRelease
	for _, entry := range candidates {
		b := entry.binding
		var session resource.Session
		if b.SessionUID == "" || b.SessionID == "" || c.GetSession(ctx, bindingSessionRef(b), &session) != nil ||
			session.Status.State != "offline" || !bindingMatches(b, entry.spec, session) {
			continue
		}
		releases = append(releases, RuntimeBindingRelease{
			SessionBindingID: b.ID, SessionID: b.SessionID, SessionUID: b.SessionUID,
			Close: BindingClose{At: now(), ResourceVersion: session.Metadata.ResourceVersion, Method: "runtime_stopped"},
		})
	}
	return releases
}

func saveRelatedReleases(ctx context.Context, tx *sql.Tx, releases []RuntimeBindingRelease) error {
	for _, release := range releases {
		raw, err := json.Marshal(release.Close)
		if err != nil {
			return storageError()
		}
		_, err = tx.ExecContext(ctx, `UPDATE session_bindings SET body=json_set(body,'$.close',json(?))
 WHERE id=? AND state='revoked' AND json_type(body,'$.close') IS NULL
 AND json_extract(body,'$.sessionId')=? AND json_extract(body,'$.sessionUid')=?`,
			raw, release.SessionBindingID, release.SessionID, release.SessionUID)
		if err != nil {
			return storageError()
		}
	}
	return nil
}
