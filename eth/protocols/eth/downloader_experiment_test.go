package eth

import (
	"math/big"
	"testing"
)

func TestLoadDownloaderExperimentConfig(t *testing.T) {
	env := map[string]string{
		downloaderExperimentEnabledEnv:  "true",
		downloaderExperimentNetworkEnv:  "714",
		downloaderExperimentVictimsEnv:  "ABCDEF",
		downloaderExperimentTDOffsetEnv: "10000000",
	}
	cfg, err := loadDownloaderExperimentConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.networkID != 714 || cfg.tdOffset.Cmp(big.NewInt(10000000)) != 0 {
		t.Fatalf("unexpected experiment configuration: %#v", cfg)
	}
	if _, ok := cfg.victims["abcdef"]; !ok {
		t.Fatal("victim ID was not normalized")
	}
}

func TestLoadDownloaderExperimentConfigRejectsMissingVictim(t *testing.T) {
	env := map[string]string{
		downloaderExperimentEnabledEnv:  "true",
		downloaderExperimentNetworkEnv:  "714",
		downloaderExperimentTDOffsetEnv: "10000000",
	}
	if _, err := loadDownloaderExperimentConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected missing victim ID to be rejected")
	}
}
