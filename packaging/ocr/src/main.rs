use anyhow::{bail, Context, Result};
use serde::Serialize;
use std::cmp;
use std::env;
use std::fs;
use std::path::Path;
use tract_onnx::prelude::*;

const REC_WIDTH: u32 = 128;
const REC_HEIGHT: u32 = 32;
const STRIDE_X: u32 = 64;
const STRIDE_Y: u32 = 16;
const MIN_TEXT_WIDTH: usize = 20;

#[derive(Serialize, Debug)]
struct OcrResult {
    text: String,
    confidence: f32,
}

#[derive(Debug, Clone)]
struct TextRegion {
    x: usize,
    y: usize,
    width: usize,
    height: usize,
    text: String,
    confidence: f32,
}

fn main() -> Result<()> {
    let args: Vec<String> = env::args().collect();
    if args.len() < 3 {
        bail!("usage: ocr <image_path> <model_path> [dict_path]");
    }

    let image_path = &args[1];
    let model_path = &args[2];
    let dict_path = args.get(3).map(|s| s.as_str());

    let results = run_ocr(image_path, model_path, dict_path)?;
    let combined = combine_text(results);
    
    let result = OcrResult {
        text: combined,
        confidence: 0.0,
    };
    let out = serde_json::to_string(&result)?;
    println!("{}", out);
    Ok(())
}

fn run_ocr(image_path: &str, model_path: &str, dict_path: Option<&str>) -> Result<Vec<TextRegion>> {
    let model = tract_onnx::onnx()
        .model_for_path(model_path)
        .with_context(|| format!("failed to load model: {}", model_path))?
        .into_optimized()?
        .into_runnable()?;

    let img = image::open(image_path).with_context(|| format!("failed to open image: {:?}", image_path))?;
    let img = img.to_rgb8();
    
    let (img_width, img_height) = img.dimensions();
    
    let dict = load_dict(dict_path)?;
    let mut regions = Vec::new();

    let mut y = 0u32;
    while y < img_height {
        let mut x = 0u32;
        while x < img_width {
            let window = extract_window(&img, x, y, REC_WIDTH, REC_HEIGHT, img_width, img_height);
            
            if window.width >= MIN_TEXT_WIDTH {
                let tensor = match Tensor::from_shape(&[1, 3, REC_HEIGHT as usize, REC_WIDTH as usize], &window.data) {
                    Ok(t) => t,
                    Err(_) => {
                        x = x.saturating_add(STRIDE_X);
                        continue;
                    }
                };
                
                let result = model.run(tvec!(tensor.into()));
                if let Ok(result) = result {
                    if let Some(output) = result.get(0) {
                        if let Ok((text, confidence)) = ctc_decode(output, &dict) {
                            if !text.trim().is_empty() {
                                regions.push(TextRegion {
                                    x: window.x,
                                    y: window.y,
                                    width: window.width,
                                    height: window.height,
                                    text,
                                    confidence,
                                });
                            }
                        }
                    }
                }
            }
            
            x = x.saturating_add(STRIDE_X);
        }
        y = y.saturating_add(STRIDE_Y);
    }

    Ok(regions)
}

struct Window {
    x: usize,
    y: usize,
    width: usize,
    height: usize,
    data: Vec<f32>,
}

fn extract_window(img: &image::RgbImage, start_x: u32, start_y: u32, target_width: u32, target_height: u32, img_width: u32, img_height: u32) -> Window {
    let x = start_x as usize;
    let y = start_y as usize;
    let width = target_width as usize;
    let height = target_height as usize;
    
    let mut data = vec![0f32; 3 * width * height];
    
    for dy in 0..target_height {
        for dx in 0..target_width {
            let src_x = cmp::min(start_x + dx, img_width.saturating_sub(1));
            let src_y = cmp::min(start_y + dy, img_height.saturating_sub(1));
            
            let pixel = img.get_pixel(src_x, src_y);
            let idx = ((dy * target_width + dx) * 3) as usize;
            data[idx] = pixel[0] as f32 / 255.0;
            data[idx + 1] = pixel[1] as f32 / 255.0;
            data[idx + 2] = pixel[2] as f32 / 255.0;
        }
    }
    
    Window { x, y, width, height, data }
}

fn ctc_decode(output: &Tensor, dict: &[char]) -> Result<(String, f32)> {
    let shape = output.shape();
    if shape.len() < 3 {
        bail!("unexpected output shape: {:?}", shape);
    }

    let seq_len = shape[1];
    let num_classes = shape[2];

    let arr = output.to_array_view::<f32>()?;

    let mut result_text = String::new();
    let mut total_confidence = 0.0f32;
    let mut count = 0usize;
    let mut last_char: isize = -1;
    let mut consecutive_blanks = 0;

    for t in 0..seq_len {
        let max_idx: isize = (0..num_classes)
            .max_by(|&a, &b| arr[[0, t, a]].partial_cmp(&arr[[0, t, b]]).unwrap())
            .unwrap() as isize;

        if max_idx == 0 {
            consecutive_blanks += 1;
            if consecutive_blanks >= 2 && last_char != -1 {
                result_text.push(' ');
                last_char = -1;
            }
        } else if max_idx != last_char && (max_idx as usize) < dict.len() {
            result_text.push(dict[max_idx as usize]);
            total_confidence += arr[[0, t, max_idx as usize]];
            count += 1;
            consecutive_blanks = 0;
        }

        if max_idx != 0 {
            last_char = max_idx;
        }
    }

    let confidence = if count > 0 {
        total_confidence / count as f32
    } else {
        0.0
    };

    Ok((result_text.trim().to_string(), confidence))
}

fn combine_text(mut regions: Vec<TextRegion>) -> String {
    if regions.is_empty() {
        return String::new();
    }

    regions.sort_by_key(|r| (r.y, r.x));
    
    let mut lines: Vec<Vec<TextRegion>> = Vec::new();
    let mut current_line: Vec<TextRegion> = Vec::new();
    let mut last_y: isize = -1;
    let line_gap_threshold: isize = REC_HEIGHT as isize / 2;

    for region in regions {
        let region_y = region.y as isize;
        if current_line.is_empty() {
            current_line.push(region);
            last_y = region_y;
        } else if (region_y - last_y).abs() < line_gap_threshold {
            current_line.push(region);
            last_y = region_y;
        } else {
            if !current_line.is_empty() {
                lines.push(current_line);
            }
            current_line = vec![region];
            last_y = region_y;
        }
    }
    
    if !current_line.is_empty() {
        lines.push(current_line);
    }

    let mut result = String::new();
    for (i, line) in lines.iter().enumerate() {
        let mut sorted_line = line.clone();
        sorted_line.sort_by_key(|r| r.x);
        
        let line_text: String = sorted_line.iter()
            .filter(|r| !r.text.trim().is_empty())
            .map(|r| r.text.trim().to_string())
            .collect::<Vec<_>>()
            .join(" ");
        
        if !line_text.trim().is_empty() {
            if i > 0 {
                result.push('\n');
            }
            result.push_str(&line_text);
        }
    }

    result.trim().to_string()
}

fn load_dict(path: Option<&str>) -> Result<Vec<char>> {
    let default_dict = " 0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.,!?;:'\"-()[]{}";
    
    if let Some(dict_path) = path {
        if Path::new(dict_path).exists() {
            let content = fs::read_to_string(dict_path)?;
            return Ok(content.chars().collect());
        }
    }

    Ok(default_dict.chars().collect())
}
