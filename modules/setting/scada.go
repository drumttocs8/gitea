// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"time"

	"code.gitea.io/gitea/modules/log"
)

type SCADAConfig struct {
	Enabled        bool
	RTACPLGPath    string
	PythonPath     string
	AcRtacCmdPath  string
	CommandTimeout time.Duration
}

var SCADA = SCADAConfig{
	Enabled:        false,
	PythonPath:     "python",
	CommandTimeout: 10 * time.Minute,
}

func loadScadaFrom(rootCfg ConfigProvider) {
	sec := rootCfg.Section("scada")
	SCADA.Enabled = sec.Key("ENABLED").MustBool(false)
	SCADA.RTACPLGPath = sec.Key("RTAC_PLG_PATH").String()
	SCADA.PythonPath = sec.Key("PYTHON_PATH").MustString(SCADA.PythonPath)
	SCADA.AcRtacCmdPath = sec.Key("ACRTACMD_PATH").String()
	SCADA.CommandTimeout = sec.Key("COMMAND_TIMEOUT").MustDuration(SCADA.CommandTimeout)

	if SCADA.Enabled && SCADA.RTACPLGPath == "" {
		log.Warn("SCADA is enabled but RTAC_PLG_PATH is not set")
	}
}
