package subtitles

// LibraryUpdates returns the durable outbox of installed subtitle changes.
// The caller must enqueue a change durably before acknowledging it.
func (m *Manager) LibraryUpdates() (map[string]string, error) {
	out := map[string]string{}
	if m == nil || m.store == nil {
		return out, nil
	}
	rows, err := m.store.db.Query("SELECT path,revision FROM subtitle_library_updates")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path, revision string
		if err := rows.Scan(&path, &revision); err != nil {
			return nil, err
		}
		out[path] = revision
	}
	return out, rows.Err()
}

func (m *Manager) AcknowledgeLibraryUpdate(path, revision string) error {
	// A newer install for the same file must survive an older acknowledgement.
	_, err := m.store.db.Exec("DELETE FROM subtitle_library_updates WHERE path=? AND revision=?", path, revision)
	return err
}
