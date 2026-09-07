package compiler

// AdvisoryCI is opt-in. An absent profile retains the existing policy behavior.
func AdvisoryCI(document CompiledPolicyDocument) bool {
	delivery, _ := document.Effective["delivery"].(map[string]any)
	return delivery["profile"] == "continuous-development"
}
