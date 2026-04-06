use anyhow::{bail, Context, Result};
use image::imageops::FilterType;
use serde::Serialize;
use std::env;
use std::fs;
use std::path::Path;
use tract_onnx::prelude::*;

const INPUT_SIZE: u32 = 224;

#[derive(Serialize)]
struct ResultPayload {
    label: String,
    top5: Vec<LabelScore>,
}

#[derive(Serialize)]
struct LabelScore {
    label: String,
    score: f32,
}

fn main() -> Result<()> {
    let args: Vec<String> = env::args().collect();
    if args.len() < 4 {
        bail!("usage: vision <image_path> <model_path> <labels_path>");
    }

    let image_path = &args[1];
    let model_path = &args[2];
    let labels_path = &args[3];

    let labels = load_labels(labels_path)?;
    let input = load_image(image_path)?;

    let model = tract_onnx::onnx()
        .model_for_path(model_path)
        .with_context(|| format!("failed to load model: {}", model_path))?
        .with_input_fact(0, InferenceFact::dt_shape(f32::datum_type(), tvec!(1, 3, INPUT_SIZE as usize, INPUT_SIZE as usize)))?
        .into_optimized()?
        .into_runnable()?;

    let result = model.run(tvec!(input.into()))?;
    let scores = result[0].to_array_view::<f32>()?;
    let scores = scores.iter().cloned().collect::<Vec<f32>>();

    let mut indexed: Vec<(usize, f32)> = scores.into_iter().enumerate().collect();
    indexed.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));

    let top5 = indexed
        .iter()
        .take(5)
        .map(|(idx, score)| LabelScore {
            label: labels.get(*idx).cloned().unwrap_or_else(|| "unknown".to_string()),
            score: *score,
        })
        .collect::<Vec<_>>();

    let label = top5
        .first()
        .map(|l| l.label.clone())
        .unwrap_or_else(|| "unknown".to_string());

    let payload = ResultPayload { label, top5 };
    let out = serde_json::to_string(&payload)?;
    println!("{}", out);
    Ok(())
}

fn load_labels<P: AsRef<Path>>(path: P) -> Result<Vec<String>> {
    let content = fs::read_to_string(&path)?;
    let labels = content
        .lines()
        .map(|l| l.trim().to_string())
        .filter(|l| !l.is_empty())
        .collect::<Vec<_>>();
    if labels.is_empty() {
        bail!("labels file is empty");
    }
    Ok(labels)
}

fn load_image<P: AsRef<Path>>(path: P) -> Result<Tensor> {
    let img = image::open(&path).with_context(|| format!("failed to open image: {:?}", path.as_ref()))?;
    let img = img.to_rgb8();
    let img = image::imageops::resize(&img, INPUT_SIZE, INPUT_SIZE, FilterType::Triangle);

    let (w, h) = img.dimensions();
    if w != INPUT_SIZE || h != INPUT_SIZE {
        bail!("failed to resize image");
    }

    let mut data = vec![0f32; 3 * INPUT_SIZE as usize * INPUT_SIZE as usize];
    for y in 0..INPUT_SIZE {
        for x in 0..INPUT_SIZE {
            let pixel = img.get_pixel(x, y);
            let idx = (0usize * INPUT_SIZE as usize * INPUT_SIZE as usize)
                + (y as usize * INPUT_SIZE as usize)
                + (x as usize);
            data[idx] = (pixel[0] as f32 - 127.0) / 128.0;
            let idx = (1usize * INPUT_SIZE as usize * INPUT_SIZE as usize)
                + (y as usize * INPUT_SIZE as usize)
                + (x as usize);
            data[idx] = (pixel[1] as f32 - 127.0) / 128.0;
            let idx = (2usize * INPUT_SIZE as usize * INPUT_SIZE as usize)
                + (y as usize * INPUT_SIZE as usize)
                + (x as usize);
            data[idx] = (pixel[2] as f32 - 127.0) / 128.0;
        }
    }

    let tensor = Tensor::from_shape(&[1, 3, INPUT_SIZE as usize, INPUT_SIZE as usize], &data)
        .context("failed to create tensor")?;
    Ok(tensor)
}
