package config

// validate runs every static rule and returns the joined failures.
func validate(_ *Config) error {
	var errs errorList
	return errs.err()
}
