package runtime

func PermissionAllowed(answer map[string]any) (bool, error) {
	if len(answer) != 2 || answer["outcome"] != "selected" {
		return false, InvalidArgument("expected selected permission option")
	}
	switch answer["optionId"] {
	case "allow-once":
		return true, nil
	case "reject-once":
		return false, nil
	}
	return false, InvalidArgument("invalid permission option")
}
