pub mod config;
pub mod events;
pub mod nucleus;
pub mod sandbox;

use std::fmt;

pub type RunId = String;
pub type Timestamp = String;

pub type Result<T> = std::result::Result<T, ContractError>;

#[derive(Debug)]
pub enum ContractError {
    Backend(String),
    Json(serde_json::Error),
    Yaml(serde_yaml::Error),
}

impl fmt::Display for ContractError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Backend(message) => write!(f, "sandbox backend error: {message}"),
            Self::Json(error) => write!(f, "json contract error: {error}"),
            Self::Yaml(error) => write!(f, "yaml contract error: {error}"),
        }
    }
}

impl std::error::Error for ContractError {}

impl From<serde_json::Error> for ContractError {
    fn from(error: serde_json::Error) -> Self {
        Self::Json(error)
    }
}

impl From<serde_yaml::Error> for ContractError {
    fn from(error: serde_yaml::Error) -> Self {
        Self::Yaml(error)
    }
}
