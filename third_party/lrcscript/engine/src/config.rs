use crate::types::LyricsKind;
use serde::Deserialize;
use std::{collections::BTreeMap, time::Duration};

#[derive(Default, Deserialize)]
#[serde(transparent)]
pub struct ProviderParams(pub BTreeMap<String, String>);
impl ProviderParams {
    pub fn get(&self, key: &str) -> Option<&str> {
        self.0.get(key).map(String::as_str)
    }
}
pub struct ProviderEntry {
    pub name: String,
    pub params: ProviderParams,
}
pub struct PluginConfig {
    pub lyrics_type_priority: Vec<LyricsKind>,
    pub duration_tolerance: Duration,
    pub require_album_match: bool,
    pub instrumental_text: Option<String>,
    pub strip_section_labels: bool,
}
impl Default for PluginConfig {
    fn default() -> Self {
        Self {
            lyrics_type_priority: vec![
                LyricsKind::Lrc,
                LyricsKind::Ttml,
                LyricsKind::Elrc,
                LyricsKind::Lyricsfile,
                LyricsKind::Plain,
            ],
            duration_tolerance: Duration::from_secs(3),
            require_album_match: false,
            instrumental_text: Some("Instrumental".into()),
            strip_section_labels: false,
        }
    }
}
