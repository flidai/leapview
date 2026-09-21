package hostinstall

import "github.com/flidai/leapview/internal/app/cli/composectl"

func upgradeComposeControl(controller *composectl.Controller) UpgradeControl {
	return UpgradeControl{
		ConfiguredImage: controller.ConfiguredImage,
		UpdateImage:     controller.UpdateImage,
		Start:           controller.Start,
		RunningImage:    controller.RunningImage,
	}
}
