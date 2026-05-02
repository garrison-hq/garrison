package objstore

// MaxCompanyMDBytes caps a single Company.md object at 64 KB per
// spec FR-642 third bullet. The wireframe sample ("842 lines") fits
// comfortably; this bound prevents accidental large-paste blow-out
// while leaving headroom for real CEO-strategic content.
const MaxCompanyMDBytes = 64 * 1024

// CheckSize returns ErrTooLarge if content exceeds the cap. Returns
// nil for any size at-or-under, including zero (empty Company.md is
// permitted; the editor's empty-state UX handles it per FR-624).
func CheckSize(content []byte) error {
	if len(content) > MaxCompanyMDBytes {
		return ErrTooLarge
	}
	return nil
}
