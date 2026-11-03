use serde::{Deserialize, Serialize};
#[derive(Debug, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct TrackInfo {
    pub id: String,
    pub title: String,
    pub artist: String,
    pub artists: Vec<Artist>,
    pub album: String,
    pub duration: f32,
}
#[derive(Debug, Default, Deserialize, Serialize)]
pub struct Artist {
    pub name: String,
}
pub struct LyricsText {
    pub lang: String,
    pub text: String,
}
pub struct GetLyricsResponse {
    pub lyrics: Vec<LyricsText>,
}
#[derive(Debug)]
pub struct Error(String);
impl Error {
    pub fn new(message: impl Into<String>) -> Self {
        Self(message.into())
    }
}
