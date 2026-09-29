package pdf

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// parseImageList reads `pdfimages -list` output and estimates each image's
// displayed size in PDF points without extracting image data to disk.
func parseImageList(data []byte) (map[int][]pdfImage, error) {
	return parseImageListing(data, maxPDFImages)
}

func parseImageListing(data []byte, maxImages int) (map[int][]pdfImage, error) {
	images := make(map[int][]pdfImage)
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 15 || !strings.EqualFold(fields[2], "image") {
			continue
		}
		page, pageErr := strconv.Atoi(fields[0])
		pixelWidth, widthErr := strconv.ParseFloat(fields[3], 64)
		pixelHeight, heightErr := strconv.ParseFloat(fields[4], 64)
		xppi, xppiErr := strconv.ParseFloat(fields[len(fields)-4], 64)
		yppi, yppiErr := strconv.ParseFloat(fields[len(fields)-3], 64)
		if pageErr != nil || widthErr != nil || heightErr != nil || xppiErr != nil || yppiErr != nil {
			return nil, strconv.ErrSyntax
		}
		if page < 1 || pixelWidth <= 0 || pixelHeight <= 0 || xppi <= 0 || yppi <= 0 ||
			math.IsInf(pixelWidth, 0) || math.IsInf(pixelHeight, 0) || math.IsInf(xppi, 0) || math.IsInf(yppi, 0) ||
			math.IsNaN(pixelWidth) || math.IsNaN(pixelHeight) || math.IsNaN(xppi) || math.IsNaN(yppi) {
			return nil, strconv.ErrRange
		}
		width := pixelWidth * 72 / xppi
		height := pixelHeight * 72 / yppi
		if math.IsInf(width, 0) || math.IsInf(height, 0) || math.IsNaN(width) || math.IsNaN(height) {
			return nil, strconv.ErrRange
		}
		if count >= maxImages {
			return nil, fmt.Errorf("%w: PDF has more than %d images", ErrResourceLimit, maxImages)
		}
		count++
		images[page] = append(images[page], pdfImage{width: width, height: height})
	}
	return images, nil
}
