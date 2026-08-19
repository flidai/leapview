package app

func workloadController(current *workloadControl) workloadControl {
	if current == nil {
		return nil
	}
	return *current
}
