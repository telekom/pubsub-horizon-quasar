package config

type ProvisioningConfiguration struct {
	Enabled  bool   `mapstructure:"enabled"`
	Port     int    `mapstructure:"port"`
	LogLevel string `mapstructure:"logLevel"`
}
