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
