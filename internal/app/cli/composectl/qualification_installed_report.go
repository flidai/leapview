package composectl

type qualificationInstalledReport struct {
	SchemaVersion  int                          `json:"schemaVersion"`
	Result         string                       `json:"result"`
	Image          string                       `json:"image"`
	Architecture   string                       `json:"architecture"`
	StartedAt      string                       `json:"startedAt"`
	CompletedAt    string                       `json:"completedAt"`
	ElapsedSeconds int64                        `json:"elapsedSeconds"`
	Phases         []qualificationPhaseEvidence `json:"phases"`
	Assertions     struct {
		OneTimeCredentials   bool `json:"oneTimeCredentials"`
		BrowserJourney       bool `json:"browserJourney"`
		PerformanceBudgets   bool `json:"performanceBudgets"`
		GovernedQuery        bool `json:"governedQuery"`
		AuditedDenial        bool `json:"auditedDenial"`
		InterruptionRecovery bool `json:"interruptionRecovery"`
		RestartPersistence   bool `json:"restartPersistence"`
		MultiNodeProcess     bool `json:"multiNodeProcess"`
		UpgradePersistence   bool `json:"upgradePersistence"`
		NativePostgresOnly   bool `json:"nativePostgresOnly"`
	} `json:"assertions"`
	MultiNode *qualificationMultiNodeReport `json:"multiNode,omitempty"`
}
