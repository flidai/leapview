package servingstate

type RetentionPolicy struct {
	ProtectActive              bool
	ProtectDraining            bool
	RequireApplyForDestructive bool
}
