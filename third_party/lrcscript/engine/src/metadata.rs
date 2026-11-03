//! Read audio properties and tags in the persistent helper, without subprocesses.
use crate::track::{Artist, TrackInfo};
use lofty::{
    config::ParseOptions,
    file::{AudioFile, TaggedFileExt},
    probe::Probe,
    tag::{Accessor, ItemKey},
};
use std::path::Path;

pub fn read(path: &Path) -> Result<TrackInfo, String> {
    let file = Probe::open(path)
        .map_err(|error| format!("cannot open audio: {error}"))?
        .options(ParseOptions::new().read_cover_art(false))
        .read()
        .map_err(|error| format!("cannot read audio metadata: {error}"))?;
    let tag = file
        .primary_tag()
        .or_else(|| file.first_tag())
        .ok_or_else(|| "audio has no metadata tags".to_string())?;
    let title = tag.title().unwrap_or_default().trim().to_string();
    let mut artists: Vec<String> = tag
        .get_strings(ItemKey::TrackArtist)
        .flat_map(|artist| artist.split(';'))
        .map(str::trim)
        .filter(|artist| !artist.is_empty())
        .map(str::to_string)
        .collect();
    if artists.is_empty() {
        artists = tag
            .get_strings(ItemKey::AlbumArtist)
            .flat_map(|artist| artist.split(';'))
            .map(str::trim)
            .filter(|artist| !artist.is_empty())
            .map(str::to_string)
            .collect();
    }
    let duration = file.properties().duration().as_secs_f32();
    if title.is_empty() || artists.is_empty() || !duration.is_finite() || duration <= 0.0 {
        return Err("artist/title tags and a positive duration are required".into());
    }
    Ok(TrackInfo {
        id: path.to_string_lossy().into_owned(),
        title,
        artist: artists.join("; "),
        artists: artists.into_iter().map(|name| Artist { name }).collect(),
        album: tag.album().unwrap_or_default().trim().to_string(),
        duration,
    })
}
