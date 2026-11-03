use crate::track::TrackInfo;
use crate::{
    config::{PluginConfig, ProviderParams},
    ext::TrackInfoExt,
    matching,
    providers::{LyricsProvider, ProviderResult, error::ProviderError, http::Http},
    types::{Lyrics, LyricsKind},
};
use serde::Deserialize;

const DEFAULT_BASE_URL: &str = "https://lrclib.net";

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Record {
    #[serde(default)]
    track_name: String,
    #[serde(default)]
    artist_name: String,
    #[serde(default)]
    album_name: String,
    synced_lyrics: Option<String>,
    plain_lyrics: Option<String>,
    #[serde(default)]
    lyricsfile: Option<String>,
    duration: Option<f32>,
    #[serde(default)]
    instrumental: bool,
}

pub struct Lrclib {
    base_url: String,
}

// Eliminate identical fallbacks within one lookup without caching any response.
fn lookup_attempts(track: &TrackInfo, cfg: &PluginConfig) -> Vec<(String, Option<String>)> {
    let mut attempts = vec![(
        track.title.clone(),
        (!track.album.trim().is_empty()).then(|| track.album.clone()),
    )];
    if !cfg.require_album_match {
        for title in [track.title.clone(), track.clean_title()] {
            let attempt = (title, None);
            if !attempts.contains(&attempt) {
                attempts.push(attempt);
            }
        }
    }
    attempts
}

impl Lrclib {
    pub fn create(params: &ProviderParams) -> Box<dyn LyricsProvider> {
        Box::new(Self {
            base_url: params
                .get("baseUrl")
                .unwrap_or(DEFAULT_BASE_URL)
                .to_string(),
        })
    }

    fn get(&self, track: &TrackInfo, cfg: &PluginConfig) -> ProviderResult<Option<Record>> {
        let attempts = lookup_attempts(track, cfg);
        // Prefer LRCLIB's own database before asking it to contact external sources.
        for endpoint in ["get-cached", "get"] {
            for (title, album) in &attempts {
                match self.try_get(track, title, album.as_deref(), endpoint)? {
                    Some(record) if matches(track, cfg, &record) => {
                        return Ok(Some(record));
                    }
                    _ => continue,
                }
            }
        }
        Ok(None)
    }

    fn try_get(
        &self,
        track: &TrackInfo,
        title: &str,
        album: Option<&str>,
        endpoint: &str,
    ) -> ProviderResult<Option<Record>> {
        let mut request = Http::get(format!("{}/api/{endpoint}", self.base_url))
            .param("artist_name", track.all_artists())
            .param("track_name", title)
            .param("duration", f64::from(track.duration).round().to_string());

        if let Some(album) = album {
            request = request.param("album_name", album);
        }

        let response = request.send()?;
        match response.status {
            200 => response.json(endpoint).map(Some),
            404 => Ok(None),
            429 => Err(response.rate_limited()),
            _ => Err(response.unexpected_status(&format!("the {endpoint} endpoint"))),
        }
    }

    fn search(&self, track: &TrackInfo, cfg: &PluginConfig) -> ProviderResult<Vec<Record>> {
        let mut request = Http::get(format!("{}/api/search", self.base_url))
            .param("track_name", track.clean_title())
            .param("artist_name", track.first_artist().unwrap_or_default());

        if cfg.require_album_match {
            request = request.param("album_name", track.album.as_str());
        }

        let response = request.send()?;
        match response.status {
            200 => response.json("search"),
            429 => Err(response.rate_limited()),
            _ => Err(response.unexpected_status("the search endpoint")),
        }
    }
}

impl LyricsProvider for Lrclib {
    fn supported_kinds(&self) -> &'static [LyricsKind] {
        &[LyricsKind::Lrc, LyricsKind::Plain, LyricsKind::Lyricsfile]
    }

    fn log_params(&self) -> Vec<(&'static str, String)> {
        vec![("baseUrl", self.base_url.clone())]
    }

    fn fetch_lyrics(
        &self,
        track: &TrackInfo,
        cfg: &PluginConfig,
    ) -> ProviderResult<Option<Lyrics>> {
        let preferred = preferred_over_plain(cfg);

        // An overloaded exact endpoint need not mean search is unavailable too.
        // Preserve the outage if search cannot recover, so the track stays deferred.
        // Rate limits and other errors still stop this provider immediately.
        let (record, get_error) = match self.get(track, cfg) {
            Ok(record) => (record, None),
            Err(error @ ProviderError::Unavailable { .. }) => (None, Some(error)),
            Err(error) => return Err(error),
        };
        let plain_fallback = match record.and_then(|record| pick_text(record, cfg)) {
            Some(plain @ Lyrics::Plain(_)) if !preferred.is_empty() => Some(plain),
            Some(lyrics) => return Ok(Some(lyrics)),
            None => None,
        };

        let mut records = match self.search(track, cfg) {
            Ok(records) => records,
            Err(_) if plain_fallback.is_some() => return Ok(plain_fallback),
            Err(error) => return Err(error),
        };
        // Search order is popularity, not recording identity: prefer the requested
        // album and closest duration among otherwise verified candidates.
        records.sort_by(|a, b| {
            let album_rank = |record: &Record| !track.matches_album(&record.album_name);
            let distance = |record: &Record| {
                record
                    .duration
                    .map(|d| (d - track.duration).abs())
                    .unwrap_or(f32::INFINITY)
            };
            album_rank(a)
                .cmp(&album_rank(b))
                .then_with(|| distance(a).total_cmp(&distance(b)))
        });
        let found = records
            .into_iter()
            .filter(|record| matches(track, cfg, record))
            .find_map(|record| match plain_fallback {
                // This branch does not check if a record is instrumental since
                // having a plain fallback means the track does have lyrics.
                Some(_) => pick_kinds(record, &preferred),
                None => pick_text(record, cfg),
            });

        match found.or(plain_fallback) {
            Some(lyrics) => Ok(Some(lyrics)),
            None => match get_error {
                Some(error) => Err(error),
                None => Ok(None),
            },
        }
    }
}

fn matches(track: &TrackInfo, cfg: &PluginConfig, record: &Record) -> bool {
    matching::duration_matches(track, cfg, record.duration.map(f64::from))
        && matching::titles_match(&track.title, &record.track_name)
        && matching::artists_match(track, &record.artist_name)
        && (!cfg.require_album_match || track.matches_album(&record.album_name))
}

fn pick_text(record: Record, cfg: &PluginConfig) -> Option<Lyrics> {
    if record.instrumental {
        return Some(Lyrics::Instrumental);
    }

    pick_kinds(record, &cfg.lyrics_type_priority)
}

fn pick_kinds(record: Record, order: &[LyricsKind]) -> Option<Lyrics> {
    let nonblank = |s: Option<String>| s.filter(|s| !s.trim().is_empty());
    let mut synced = nonblank(record.synced_lyrics);
    let mut plain = nonblank(record.plain_lyrics);
    let mut lyricsfile = nonblank(record.lyricsfile);

    order.iter().find_map(|kind| match kind {
        LyricsKind::Lrc => synced.take().map(Lyrics::Lrc),
        LyricsKind::Plain => plain.take().map(Lyrics::Plain),
        LyricsKind::Lyricsfile => lyricsfile.take().map(Lyrics::Lyricsfile),
        _ => None,
    })
}

fn preferred_over_plain(cfg: &PluginConfig) -> Vec<LyricsKind> {
    cfg.lyrics_type_priority
        .iter()
        .take_while(|&&k| k != LyricsKind::Plain)
        .copied()
        .filter(|k| matches!(k, LyricsKind::Lrc | LyricsKind::Lyricsfile))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unchanged_title_is_only_requested_once_without_album() {
        let track = TrackInfo {
            title: "Song".into(),
            album: "Album".into(),
            ..Default::default()
        };
        assert_eq!(
            lookup_attempts(&track, &PluginConfig::default()),
            vec![("Song".into(), Some("Album".into())), ("Song".into(), None)]
        );
    }
    #[test]
    fn cleaned_title_fallback_and_album_requirement_are_preserved() {
        let track = TrackInfo {
            title: "Song (Album Version)".into(),
            album: "Album".into(),
            ..Default::default()
        };
        assert_eq!(lookup_attempts(&track, &PluginConfig::default()).len(), 3);
        let cfg = PluginConfig {
            require_album_match: true,
            ..Default::default()
        };
        assert_eq!(
            lookup_attempts(&track, &cfg),
            vec![(track.title, Some(track.album))]
        );
    }

    const SYNCED: &str = "[00:00.00] Hello";
    const PLAIN: &str = "Hello";
    const LYRICSFILE: &str = "version: 1.0\nlines: []";

    fn full_record() -> Record {
        Record {
            synced_lyrics: Some(SYNCED.to_string()),
            plain_lyrics: Some(PLAIN.to_string()),
            lyricsfile: Some(LYRICSFILE.to_string()),
            duration: Some(180.0),
            ..Record::default()
        }
    }

    #[track_caller]
    fn check_pick(record: Record, priority: &[LyricsKind], expected: Option<Lyrics>) {
        let cfg = PluginConfig {
            lyrics_type_priority: priority.to_vec(),
            ..PluginConfig::default()
        };
        let described = format!("{record:?} with {priority:?}");

        assert_eq!(pick_text(record, &cfg), expected, "{described}");
    }

    #[track_caller]
    fn check_kinds(record: Record, order: &[LyricsKind], expected: Option<Lyrics>) {
        let described = format!("{record:?} with {order:?}");

        assert_eq!(pick_kinds(record, order), expected, "{described}");
    }

    #[track_caller]
    fn check_preferred(priority: &[LyricsKind], expected: &[LyricsKind]) {
        let cfg = PluginConfig {
            lyrics_type_priority: priority.to_vec(),
            ..PluginConfig::default()
        };

        assert_eq!(
            preferred_over_plain(&cfg),
            expected,
            "priority {priority:?}"
        );
    }

    #[test]
    fn highest_priority_format_available_wins() {
        check_pick(
            full_record(),
            &[LyricsKind::Lrc, LyricsKind::Plain],
            Some(Lyrics::Lrc(SYNCED.to_string())),
        );
        check_pick(
            full_record(),
            &[LyricsKind::Lyricsfile, LyricsKind::Lrc],
            Some(Lyrics::Lyricsfile(LYRICSFILE.to_string())),
        );
        check_pick(
            full_record(),
            &[LyricsKind::Plain, LyricsKind::Lrc],
            Some(Lyrics::Plain(PLAIN.to_string())),
        );
        check_pick(
            Record {
                plain_lyrics: Some(PLAIN.to_string()),
                ..Record::default()
            },
            &[LyricsKind::Lrc, LyricsKind::Plain],
            Some(Lyrics::Plain(PLAIN.to_string())),
        );
        check_pick(
            Record::default(),
            &[LyricsKind::Lrc, LyricsKind::Plain],
            None,
        );
        check_pick(full_record(), &[LyricsKind::Ttml, LyricsKind::Srt], None);
    }

    #[test]
    fn blank_lyrics_are_skipped() {
        for blank in ["", "   ", "\n\t "] {
            check_pick(
                Record {
                    synced_lyrics: Some(blank.to_string()),
                    plain_lyrics: Some(PLAIN.to_string()),
                    ..Record::default()
                },
                &[LyricsKind::Lrc, LyricsKind::Plain],
                Some(Lyrics::Plain(PLAIN.to_string())),
            );
        }
    }

    #[test]
    fn an_instrumental_track_needs_no_lyrics() {
        check_pick(
            Record {
                instrumental: true,
                ..Record::default()
            },
            &[LyricsKind::Lrc, LyricsKind::Plain],
            Some(Lyrics::Instrumental),
        );
    }

    #[test]
    fn a_narrowed_pick_ignores_the_instrumental_flag() {
        check_kinds(
            Record {
                instrumental: true,
                ..full_record()
            },
            &[LyricsKind::Lrc],
            Some(Lyrics::Lrc(SYNCED.to_string())),
        );
    }

    #[test]
    fn a_narrowed_pick_wont_fall_back_to_plain() {
        check_kinds(
            Record {
                plain_lyrics: Some(PLAIN.to_string()),
                ..Record::default()
            },
            &[LyricsKind::Lrc, LyricsKind::Lyricsfile],
            None,
        );
    }

    #[test]
    fn only_synced_formats_above_plain_are_worth_a_second_search() {
        check_preferred(
            &[LyricsKind::Lyricsfile, LyricsKind::Lrc, LyricsKind::Plain],
            &[LyricsKind::Lyricsfile, LyricsKind::Lrc],
        );
        check_preferred(
            &[LyricsKind::Lyricsfile, LyricsKind::Plain, LyricsKind::Lrc],
            &[LyricsKind::Lyricsfile],
        );
    }

    #[test]
    fn plain_first_means_nothing_is_worth_searching_for() {
        check_preferred(&[LyricsKind::Plain, LyricsKind::Lrc], &[]);
    }

    #[test]
    fn without_plain_every_synced_format_is_worth_searching() {
        check_preferred(
            &[LyricsKind::Lrc, LyricsKind::Lyricsfile],
            &[LyricsKind::Lrc, LyricsKind::Lyricsfile],
        );
    }

    #[test]
    fn unsupported_formats_are_never_preferred() {
        check_preferred(
            &[LyricsKind::Ttml, LyricsKind::Elrc, LyricsKind::Plain],
            &[],
        );
    }

    #[test]
    fn a_record_deserializes_from_the_api_response() {
        let json = r#"{
            "id": 3396226,
            "trackName": "I Want to Live",
            "duration": 233.0,
            "instrumental": false,
            "plainLyrics": "I feel your breath",
            "syncedLyrics": "[00:17.12] I feel your breath"
        }"#;

        let record: Record = serde_json::from_str(json).unwrap();

        assert_eq!(record.duration, Some(233.0));
        assert_eq!(record.plain_lyrics.as_deref(), Some("I feel your breath"));
        assert_eq!(
            record.synced_lyrics.as_deref(),
            Some("[00:17.12] I feel your breath")
        );
        assert_eq!(record.lyricsfile, None);
        assert!(!record.instrumental);
    }
}
