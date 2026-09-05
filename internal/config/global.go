package config

// globalSchema lists the tables and keys ~/.prison/config.toml accepts.
var globalSchema = tableSchema{
	"prison":     {"cage", "inmates"},
	"checkpoint": {"pager", "diff_tool"},
}

// GlobalPrison is the [prison] table of the global file.
// It sets the default cage and inmates for every box.
// A nil field means the key was absent. An empty list means
// "none" and stops the lookup.
type GlobalPrison struct {
	Cage    *string   `toml:"cage"`
	Inmates *[]string `toml:"inmates"`
}

// GlobalCheckpoint is the [checkpoint] table of the global file.
// It sets the pager and diff renderer. A nil field means the key
// was absent. An empty string means "use nothing."
type GlobalCheckpoint struct {
	Pager    *string `toml:"pager"`
	DiffTool *string `toml:"diff_tool"`
}

// Global is ~/.prison/config.toml. It holds user-level settings
// that are not tied to any project. The zero value means no
// file exists.
type Global struct {
	Prison     GlobalPrison     `toml:"prison"`
	Checkpoint GlobalCheckpoint `toml:"checkpoint"`
}

// LoadGlobal reads and validates the global config file at path.
// It takes a file path and returns a *Global and an error.
// A missing file gives an empty Global, not an error.
// Unknown tables or keys cause an error.
func LoadGlobal(path string) (*Global, error) {
	global := &Global{}
	found, err := decodeFile(path, global, globalSchema)
	if err != nil {
		return nil, err
	}
	if !found {
		return global, nil
	}
	if err := global.validate(); err != nil {
		return nil, unusable(path, err)
	}
	return global, nil
}

// validate checks every value in a decoded global file. It returns
// the first problem it finds.
func (global *Global) validate() error {
	if global.Prison.Cage != nil {
		if err := ValidateName("cage", *global.Prison.Cage); err != nil {
			return err
		}
	}
	if global.Prison.Inmates != nil {
		for _, inmate := range *global.Prison.Inmates {
			if err := ValidateName("inmate", inmate); err != nil {
				return err
			}
		}
	}
	if global.Checkpoint.Pager != nil {
		err := requireSingleLine("checkpoint.pager", *global.Checkpoint.Pager)
		if err != nil {
			return err
		}
	}
	if global.Checkpoint.DiffTool != nil {
		err := requireSingleLine(
			"checkpoint.diff_tool", *global.Checkpoint.DiffTool)
		if err != nil {
			return err
		}
	}
	return nil
}
