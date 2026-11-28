//go:build windows

package service

// EnsureServiceAccount is a no-op on Windows: the service runs under a Virtual
// Service Account (VSA) that the Service Control Manager provisions automatically
// when the service is installed, so there is no manual account to create at
// startup (PART 23 / PART 24).
func EnsureServiceAccount(username string) error {
	return nil
}

// PrepareSystemServiceAccount is a no-op on Windows because the service runs
// under a Virtual Service Account provisioned by the Service Control Manager.
func PrepareSystemServiceAccount(username string) error {
	return nil
}
