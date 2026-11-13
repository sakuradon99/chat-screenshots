package main

import (
	"image"
	"image/draw"
	"net/http"
)

type cropRegion struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (region cropRegion) apply(img image.Image) image.Image {
	bounds := image.Rect(region.X, region.Y, region.X+region.Width, region.Y+region.Height)
	if source, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok && bounds.In(img.Bounds()) {
		return source.SubImage(bounds)
	}
	// A GIF's first frame can cover only part of its logical canvas.
	cropped := image.NewNRGBA(bounds)
	draw.Draw(cropped, bounds, img, bounds.Min, draw.Src)
	return cropped
}

func (a *app) saveCrop(w http.ResponseWriter, r *http.Request) {
	var fields struct {
		Revision uint64
		Crop     *cropRegion
	}
	if err := readJSON(w, r, &fields); err != nil {
		writeError(w, err)
		return
	}
	updated, err := a.updateGroup(r.PathValue("group"), func(g *group) error {
		if fields.Revision != g.Revision {
			return &clientError{http.StatusConflict, "截图组已在其他设备上更新，请重新打开裁切。"}
		}
		region := fields.Crop
		if region != nil {
			if len(g.Images) == 0 {
				return invalid("请先添加图片。")
			}
			first := g.Images[0]
			for _, img := range g.Images {
				if img.Width != first.Width || img.Height != first.Height {
					return invalid("只有组内所有图片尺寸一致时才能批量裁切。")
				}
			}
			if region.Width < 1 || region.Height < 1 || region.X < 0 || region.Y < 0 ||
				region.Width > first.Width || region.Height > first.Height ||
				region.X > first.Width-region.Width || region.Y > first.Height-region.Height {
				return invalid("裁切区域必须在图片范围内。")
			}
		}
		g.Crop = region
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
