package store

// SeedDefaults populates the default folder layout when the store is fresh.
// No-op if any folder already exists.
func (d *DB) SeedDefaults() error {
	existing, err := d.ListFolders()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	if _, err := d.CreateFolder(NewFolder{Name: "Personal", SortOrder: 0}); err != nil {
		return err
	}
	work, err := d.CreateFolder(NewFolder{Name: "Work", SortOrder: 1})
	if err != nil {
		return err
	}
	for i, n := range []string{"Production", "Staging", "Dev"} {
		if _, err := d.CreateFolder(NewFolder{
			ParentID:  &work.ID,
			Name:      n,
			SortOrder: int64(i),
		}); err != nil {
			return err
		}
	}
	if _, err := d.CreateFolder(NewFolder{Name: "Imported", SortOrder: 2}); err != nil {
		return err
	}
	return nil
}

// SeedDefaultSnippets populates a small set of general diagnostic snippets on a
// fresh store so the snippet palette (Ctrl+Shift+P) isn't empty on first run.
// No-op if any snippet already exists, so it never re-adds after the user has
// cleaned them out. A couple use ${var} placeholders to surface that feature.
func (d *DB) SeedDefaultSnippets() error {
	existing, err := d.ListSnippets(nil)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	// Keep these to commands worth a saved snippet - non-trivial invocations or
	// ones showing the ${var} feature. Skip things nobody needs a snippet for
	// (df -h, free -h, top).
	defaults := []SnippetInput{
		{Name: "Listening ports", Body: "ss -tlnp", Tags: []string{"diag", "network"}},
		{Name: "Service status", Body: "systemctl status ${svc}", Tags: []string{"diag", "systemd"}},
		{Name: "Service logs", Body: "journalctl -u ${svc} --since ${since:-1h} --no-pager", Tags: []string{"diag", "logs"}},
		{Name: "Tail a log", Body: "tail -f ${path:-/var/log/syslog}", Tags: []string{"diag", "logs"}},
		{Name: "Kernel messages", Body: "dmesg -T | tail -40", Tags: []string{"diag", "kernel"}},
		{Name: "Top by memory", Body: "ps aux --sort=-%mem | head -12", Tags: []string{"diag", "memory"}},
		{Name: "Compose up", Body: "docker compose up -d", Tags: []string{"docker", "compose"}},
		{Name: "Compose pull + up", Body: "docker compose pull && docker compose up -d", Tags: []string{"docker", "compose"}},
		{Name: "Compose logs", Body: "docker compose logs -f --tail=100 ${service}", Tags: []string{"docker", "compose", "logs"}},
		{Name: "Docker containers", Body: "docker ps -a", Tags: []string{"docker"}},
		{Name: "Docker disk usage", Body: "docker system df", Tags: []string{"docker", "disk"}},
	}
	for _, s := range defaults {
		if _, err := d.CreateSnippet(s); err != nil {
			return err
		}
	}
	return nil
}
