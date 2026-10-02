package config

var globalSchema = tableSchema{
	"prison":     {"cage", "inmates"},
	"checkpoint": {"pager", "diff_tool"},
}

// GlobalPrison holds the default cage and inmates for every box. A nil
// field means that the key is absent. An empty list means none and stops
// the lookup.
type GlobalPrison struct {
	Cage    *string   `toml:"cage"`
	Inmates *[]string `toml:"inmates"`
}

// GlobalCheckpoint holds the pager and diff tool. A nil field means that
// the key is absent. An empty string turns the tool off.
type GlobalCheckpoint struct {
	Pager    *string `toml:"pager"`
	DiffTool *string `toml:"diff_tool"`
}

type Global struct {
	Prison     GlobalPrison     `toml:"prison"`
	Checkpoint GlobalCheckpoint `toml:"checkpoint"`
}

// LoadGlobal returns an empty Global when the file is missing.
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
