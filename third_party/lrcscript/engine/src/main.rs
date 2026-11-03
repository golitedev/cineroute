#![allow(dead_code)]
mod config;
mod ext;
mod format;
mod matching;
mod metadata;
mod providers;
mod track;
mod types;

use config::{PluginConfig, ProviderEntry, ProviderParams};
use providers::ProviderRegistry;
use serde::{Deserialize, Serialize};
use std::{
    collections::HashMap,
    io::{self, BufRead, Write},
    time::{Duration, Instant},
};
use types::{Lyrics, LyricsKind};

const PROVIDERS: &[&str] = &[
    "lrclib",
    "lyrics.ovh",
    "lrcmux",
    "kugou",
    "netease",
    "qqmusic",
    "stixoi",
    "genie",
    "binilyrics",
    "musixmatch",
];

#[derive(Clone, Serialize)]
struct Fetched {
    text: String,
    kind: String,
    sync: usize,
    provider: String,
}
impl Fetched {
    fn from_lyrics(lyrics: &Lyrics, cfg: &PluginConfig, provider: &str) -> Self {
        Self {
            text: lyrics.text(cfg).into_owned(),
            kind: lyrics.kind().slug().into(),
            sync: lyrics.sync_level().rank(),
            provider: provider.into(),
        }
    }
}
#[derive(Deserialize)]
#[serde(untagged)]
enum Input {
    Metadata { metadata_path: std::path::PathBuf },
    Lookup(Request),
}
#[derive(Deserialize)]
struct Request {
    track: track::TrackInfo,
    providers: Vec<String>,
    mode: String,
    #[serde(default)]
    synced_only: bool,
    #[serde(default)]
    strip_section_labels: bool,
    #[serde(default)]
    require_album_match: bool,
    tolerance: f64,
    delay: f64,
    timeout: u64,
    retries: usize,
    #[serde(default)]
    params: HashMap<String, ProviderParams>,
}
#[derive(Serialize)]
struct Attempt {
    provider: String,
    status: String,
    error: Option<String>,
    retry_after: Option<i64>,
}
#[derive(Serialize)]
struct Reply {
    lyrics: Option<Fetched>,
    attempts: Vec<Attempt>,
    unavailable: bool,
    retry_after: Option<i64>,
}

fn process(
    request: Request,
    registry: &ProviderRegistry,
    cooldowns: &mut HashMap<String, Instant>,
) -> Reply {
    let mut cfg = PluginConfig {
        duration_tolerance: Duration::from_secs_f64(request.tolerance),
        require_album_match: request.require_album_match,
        strip_section_labels: request.strip_section_labels,
        ..Default::default()
    };
    if request.synced_only {
        cfg.lyrics_type_priority
            .retain(|&kind| kind != LyricsKind::Plain);
    }
    providers::http::configure(request.delay, request.timeout, request.retries);
    let mut reply = Reply {
        lyrics: None,
        attempts: Vec::new(),
        unavailable: false,
        retry_after: None,
    };
    let mut successful_lookup = false;
    for name in request.providers {
        if let Some(until) = cooldowns
            .get(&name)
            .filter(|until| **until > Instant::now())
        {
            reply.attempts.push(Attempt {
                provider: name,
                status: "cooldown".into(),
                error: Some(format!(
                    "retry in {}s",
                    until.saturating_duration_since(Instant::now()).as_secs() + 1
                )),
                retry_after: Some(
                    until.saturating_duration_since(Instant::now()).as_secs() as i64 + 1,
                ),
            });
            continue;
        }
        let mut attempt = Attempt {
            provider: name.clone(),
            status: String::new(),
            error: None,
            retry_after: None,
        };
        let value = {
            eprintln!("  Trying {name}...");
            let params = request.params.get(&name);
            let entry = ProviderEntry {
                name: name.clone(),
                params: ProviderParams(params.map(|p| p.0.clone()).unwrap_or_default()),
            };
            match registry.create(&entry) {
                None => {
                    attempt.status = "error".into();
                    attempt.error = Some("unknown provider".into());
                    None
                }
                Some(provider) => {
                    // Avoid plain-only endpoints when plain lyrics were explicitly disabled.
                    if !provider
                        .supported_kinds()
                        .iter()
                        .any(|kind| cfg.lyrics_type_priority.contains(kind))
                    {
                        attempt.status = "disabled-format".into();
                        reply.attempts.push(attempt);
                        continue;
                    }
                    match provider.fetch_lyrics(&request.track, &cfg) {
                        Ok(lyrics) => {
                            successful_lookup = true;
                            attempt.status = "missing".into();
                            lyrics.and_then(|mut lyrics| {
                                lyrics.sanitize(&cfg);
                                if lyrics.is_empty() {
                                    return None;
                                }
                                attempt.status = "found".into();
                                Some(Fetched::from_lyrics(&lyrics, &cfg, &name))
                            })
                        }
                        Err(error) => {
                            attempt.status = if error.cooldown_seconds().is_some() {
                                "error"
                            } else {
                                "rejected"
                            }
                            .into();
                            successful_lookup |= error.cooldown_seconds().is_none();
                            attempt.error = Some(error.to_string());
                            // Only outages/blocking/rate limits apply across tracks.
                            // A mismatch or malformed lyric belongs to this result alone.
                            if let Some(seconds) = error.cooldown_seconds() {
                                attempt.retry_after = Some(seconds);
                                cooldowns.insert(
                                    name.clone(),
                                    Instant::now() + Duration::from_secs(seconds as u64),
                                );
                            }
                            None
                        }
                    }
                }
            }
        };
        reply.attempts.push(attempt);
        if let Some(value) = value {
            let instrumental = value.kind == "instrumental";
            let rank = |v: &Fetched| -> (usize, usize) {
                let kind = LyricsKind::from_slug(&v.kind).unwrap();
                let format = cfg
                    .lyrics_type_priority
                    .iter()
                    .position(|&k| k == kind)
                    .unwrap_or(99);
                if request.mode == "type" {
                    (format, v.sync)
                } else {
                    (v.sync, 0)
                }
            };
            if instrumental
                || reply
                    .lyrics
                    .as_ref()
                    .is_none_or(|best| rank(&value) < rank(best))
            {
                reply.lyrics = Some(value);
            }
            if request.mode == "priority"
                || instrumental
                || (request.mode == "sync" && reply.lyrics.as_ref().is_some_and(|v| v.sync == 0))
                || (request.mode == "type"
                    && reply.lyrics.as_ref().is_some_and(|v| {
                        cfg.lyrics_type_priority
                            .first()
                            .is_some_and(|kind| v.kind == kind.slug())
                    }))
            {
                break;
            }
        }
    }
    if reply.lyrics.is_none() {
        reply.retry_after = reply
            .attempts
            .iter()
            .filter_map(|attempt| attempt.retry_after)
            .min();
    }
    reply.unavailable = !successful_lookup && reply.lyrics.is_none() && reply.retry_after.is_some();
    reply
}

fn main() {
    if std::env::args().any(|a| a == "--providers") {
        println!("{}", PROVIDERS.join(","));
        return;
    }
    let mut registry = ProviderRegistry::new();
    providers::register_providers(&mut registry);
    let mut cooldowns = HashMap::new();
    for line in io::stdin().lock().lines() {
        let output = match line {
            Ok(line) => match serde_json::from_str::<Input>(&line) {
                Ok(Input::Metadata { metadata_path }) => match metadata::read(&metadata_path) {
                    Ok(track) => serde_json::json!({"track": track}),
                    Err(error) => serde_json::json!({"error": error}),
                },
                Ok(Input::Lookup(request)) => {
                    if !request.tolerance.is_finite()
                        || request.tolerance <= 0.0
                        || Duration::try_from_secs_f64(request.tolerance).is_err()
                        || !request.delay.is_finite()
                        || request.delay < 0.0
                        || !request.track.duration.is_finite()
                        || request.track.duration <= 0.0
                        || Duration::try_from_secs_f32(request.track.duration).is_err()
                        || request.timeout == 0
                        || request.providers.is_empty()
                        || request
                            .providers
                            .iter()
                            .any(|p| !PROVIDERS.contains(&p.as_str()))
                        || !["sync", "type", "priority"].contains(&request.mode.as_str())
                    {
                        serde_json::json!({"error": "invalid request settings", "fatal": true})
                    } else {
                        serde_json::to_value(process(request, &registry, &mut cooldowns)).unwrap()
                    }
                }
                Err(error) => serde_json::json!({"error": error.to_string(), "fatal": true}),
            },
            Err(_) => break,
        };
        println!("{output}");
        if io::stdout().flush().is_err() {
            break;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use providers::{LyricsProvider, error::ProviderResult};

    struct Fixture {
        kind: String,
    }
    impl LyricsProvider for Fixture {
        fn supported_kinds(&self) -> &'static [LyricsKind] {
            &[
                LyricsKind::Lrc,
                LyricsKind::Elrc,
                LyricsKind::Ttml,
                LyricsKind::Lyricsfile,
                LyricsKind::Plain,
            ]
        }
        fn fetch_lyrics(
            &self,
            _: &track::TrackInfo,
            _: &PluginConfig,
        ) -> ProviderResult<Option<Lyrics>> {
            Ok(match self.kind.as_str() {
                "lrc" => Some(Lyrics::Lrc("[00:01.00]Words".into())),
                "elrc" => Some(Lyrics::Elrc("[00:01.00]<00:01.00>Words<00:02.00>".into())),
                "ttml" => Some(Lyrics::Ttml(
                    "<tt itunes:timing=\"Word\"><body>Words</body></tt>".into(),
                )),
                "lyricsfile" => Some(Lyrics::Lyricsfile("version: 1.0\nlines: []".into())),
                "plain" => Some(Lyrics::Plain("Words".into())),
                "instrumental" => Some(Lyrics::Instrumental),
                _ => None,
            })
        }
    }
    fn fixture(params: &ProviderParams) -> Box<dyn LyricsProvider> {
        Box::new(Fixture {
            kind: params.get("kind").unwrap_or("missing").into(),
        })
    }
    fn lookup(kinds: &[&str], mode: &str) -> Reply {
        let mut registry = ProviderRegistry::new();
        let mut params = serde_json::Map::new();
        let names: Vec<_> = kinds
            .iter()
            .enumerate()
            .map(|(i, kind)| {
                let name = format!("provider{i}");
                registry.register(&name, fixture);
                params.insert(name.clone(), serde_json::json!({"kind": kind}));
                name
            })
            .collect();
        let request = serde_json::from_value(serde_json::json!({
            "track": {"artist": "Artist", "title": "Song", "duration": 180},
            "providers": names, "params": params, "mode": mode,
            "tolerance": 3, "delay": 0, "timeout": 1, "retries": 0
        }))
        .unwrap();
        process(request, &registry, &mut HashMap::new())
    }
    #[test]
    fn lrc_beats_word_formats_and_stops_further_requests() {
        for alternative in ["elrc", "ttml", "lyricsfile"] {
            let reply = lookup(&[alternative, "lrc", "ttml"], "type");
            assert_eq!(reply.lyrics.unwrap().kind, "lrc");
            assert_eq!(reply.attempts.len(), 2);
        }
        assert_eq!(lookup(&["lrc", "elrc"], "type").attempts.len(), 1);
    }
    #[test]
    fn other_formats_remain_fallbacks() {
        let reply = lookup(&["missing", "elrc", "ttml"], "type");
        assert_eq!(reply.lyrics.unwrap().kind, "ttml");
        assert_eq!(reply.attempts.len(), 3);
    }
    #[test]
    fn priority_returns_first_and_sync_prefers_timing() {
        assert_eq!(
            lookup(&["plain", "lrc"], "priority").lyrics.unwrap().kind,
            "plain"
        );
        assert_eq!(
            lookup(&["plain", "lrc"], "sync").lyrics.unwrap().kind,
            "lrc"
        );
        let reply = lookup(&["elrc", "lrc"], "sync");
        assert_eq!(reply.lyrics.unwrap().kind, "elrc");
        assert_eq!(reply.attempts.len(), 1);
    }
    #[test]
    fn ties_prefer_earlier_provider() {
        assert_eq!(
            lookup(&["lrc", "lrc"], "sync").lyrics.unwrap().provider,
            "provider0"
        );
    }
    #[test]
    fn instrumental_and_missing_results() {
        assert_eq!(
            lookup(&["missing", "instrumental"], "type")
                .lyrics
                .unwrap()
                .kind,
            "instrumental"
        );
        let reply = lookup(&["missing", "missing"], "type");
        assert!(reply.lyrics.is_none());
        assert!(!reply.unavailable);
        assert_eq!(reply.attempts.len(), 2);
    }
}
