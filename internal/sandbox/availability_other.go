//go:build !darwin && !linux

package sandbox

func currentAvailability() Availability {
	return Availability{Reason: "no strict sandbox backend is implemented for this platform"}
}
