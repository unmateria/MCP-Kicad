package providers

// The repository sources. Each is a literal: adding one is a new entry here
// plus a Register call, with no new code.
//
// Every URL, branch name and directory layout below was measured against the
// live repository on 2026-08-09, not recalled. The licences are what the
// repository itself states; where it states nothing conclusive the field says
// so, and import_part reports it verbatim rather than deciding for you.

func init() {
	// CERN's Open Hardware libraries: symbols converted from the Altium
	// originals, under a permissive licence, and the best answer to "I know
	// the manufacturer part number, what symbol do I use".
	//
	// Measured limitation: the part-number-to-footprint mapping lives in
	// CERN.sqlite, a 14 MB KiCad database library. Reading it would mean
	// taking a SQLite driver as a dependency, so this provider indexes the
	// symbols and leaves the footprint to be resolved from the installed KiCad
	// libraries. The symbols' Footprint property is empty here — that is the
	// source's shape, not a bug in the reader.
	Register(func(env Env) Provider {
		return &repoProvider{env: env, src: repoSource{
			name:        "cern",
			description: "CERN Open Hardware KiCad libraries — symbols for ~17k real part numbers",
			license:     "CERN-OHL-P-2.0",
			homepage:    "https://gitlab.com/ohwr/cern-kicad-libs",
			listTree:    gitlabTree("ohwr/cern-kicad-libs", "master"),
			rawURL:      gitlabRaw("ohwr/cern-kicad-libs", "master"),
			symbolDir:   "SchLib/",
			// PcbLib is deliberately NOT indexed: its symbols carry an empty
			// Footprint property, so there is nothing to pair a footprint
			// with, and listing thousands of files to answer no question
			// costs a minute on every refresh.
			footprintDir: "",
		}}
	})

	// JLCPCB's assembly catalogue, symbol + footprint + STEP, all matched.
	// The highest-quality source here: MIT, and every part is one you can
	// actually have assembled.
	Register(func(env Env) Provider {
		return &repoProvider{env: env, src: repoSource{
			name:         "jlcpcb",
			description:  "JLCPCB basic & preferred parts — matched symbol, footprint and 3D model",
			license:      "MIT",
			homepage:     "https://github.com/CDFER/JLCPCB-Kicad-Library",
			listTree:     githubTree("CDFER", "JLCPCB-Kicad-Library", "main"),
			rawURL:       githubRaw("CDFER", "JLCPCB-Kicad-Library", "main"),
			symbolDir:    "symbols/",
			footprintDir: "footprints/",
			fallbackSymbolLibs: []string{
				"symbols/JLCPCB-Analog.kicad_sym",
				"symbols/JLCPCB-Capacitors.kicad_sym",
				"symbols/JLCPCB-Connectors_Buttons.kicad_sym",
				"symbols/JLCPCB-Crystals.kicad_sym",
				"symbols/JLCPCB-Diode-Packages.kicad_sym",
				"symbols/JLCPCB-Diodes.kicad_sym",
				"symbols/JLCPCB-Extended.kicad_sym",
				"symbols/JLCPCB-ICs.kicad_sym",
				"symbols/JLCPCB-Inductors.kicad_sym",
				"symbols/JLCPCB-Interface.kicad_sym",
				"symbols/JLCPCB-MCUs.kicad_sym",
				"symbols/JLCPCB-Manufacturing.kicad_sym",
				"symbols/JLCPCB-Memory.kicad_sym",
				"symbols/JLCPCB-Optocouplers.kicad_sym",
				"symbols/JLCPCB-Power.kicad_sym",
				"symbols/JLCPCB-Resistors.kicad_sym",
				"symbols/JLCPCB-Transformers.kicad_sym",
				"symbols/JLCPCB-Transistor-Packages.kicad_sym",
				"symbols/JLCPCB-Transistors.kicad_sym",
				"symbols/JLCPCB-Variable-Resistors.kicad_sym",
			},
		}}
	})

	// Digi-Key's library, community-maintained fork updated for KiCad 10.
	// 150 category libraries covering the catalogue's common parts.
	Register(func(env Env) Provider {
		return &repoProvider{env: env, src: repoSource{
			name:         "digikey-lib",
			description:  "Digi-Key KiCad library (v10 fork) — 150 category libraries, symbol + footprint",
			license:      "see LICENSE.md in the repository",
			homepage:     "https://github.com/IamPhytan/digikey-kicad-library",
			listTree:     githubTree("IamPhytan", "digikey-kicad-library", "v10"),
			rawURL:       githubRaw("IamPhytan", "digikey-kicad-library", "v10"),
			symbolDir:    "digikey-symbols/",
			footprintDir: "digikey-footprints.pretty/",
		}}
	})

	// Espressif's own library: the ESP32 family, straight from the vendor.
	Register(func(env Env) Provider {
		return &repoProvider{env: env, src: repoSource{
			name:               "espressif",
			description:        "Espressif official library — ESP32 modules and dev boards",
			license:            "see LICENSE.md in the repository",
			homepage:           "https://github.com/espressif/kicad-libraries",
			listTree:           githubTree("espressif", "kicad-libraries", "main"),
			rawURL:             githubRaw("espressif", "kicad-libraries", "main"),
			symbolDir:          "symbols/",
			footprintDir:       "footprints/",
			fallbackSymbolLibs: []string{"symbols/Espressif.kicad_sym"},
		}}
	})

	// SparkFun's library: breakout boards, sensors and connectors that rarely
	// appear anywhere else.
	Register(func(env Env) Provider {
		return &repoProvider{env: env, src: repoSource{
			name:         "sparkfun",
			description:  "SparkFun KiCad libraries — breakouts, sensors, connectors",
			license:      "CC-SA-4.0 (per the project's README)",
			homepage:     "https://github.com/sparkfun/SparkFun-KiCad-Libraries",
			listTree:     githubTree("sparkfun", "SparkFun-KiCad-Libraries", "main"),
			rawURL:       githubRaw("sparkfun", "SparkFun-KiCad-Libraries", "main"),
			symbolDir:    "symbols/",
			footprintDir: "footprints/",
		}}
	})
}
