package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hpinc/go3mf"
	"github.com/hschendel/stl"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <input.3mf> [output.stl]\n", os.Args[0])
		os.Exit(1)
	}

	inputPath := os.Args[1]
	var outputPath string
	if len(os.Args) >= 3 {
		outputPath = os.Args[2]
	} else {
		outputPath = replaceExt(inputPath, ".stl")
	}

	if err := convert3MFToSTL(inputPath, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Converted %s -> %s\n", inputPath, outputPath)
}

func replaceExt(path, newExt string) string {
	return path[:len(path)-len(filepath.Ext(path))] + newExt
}

const contentTypesName = "[Content_Types].xml"

// contentTypesXML matches OPC [Content_Types].xml for parsing and writing.
type contentTypesXML struct {
	XMLName xml.Name `xml:"Types"`
	XML     string   `xml:"xmlns,attr"`
	Default []struct {
		Extension   string `xml:"Extension,attr"`
		ContentType string `xml:"ContentType,attr"`
	} `xml:"Default"`
	Override []struct {
		PartName    string `xml:"PartName,attr"`
		ContentType string `xml:"ContentType,attr"`
	} `xml:"Override"`
}

// open3MF opens the 3MF file, patching [Content_Types].xml if needed so that
// every part has a content type (fixes slicer-generated 3MFs that add e.g.
// /Metadata/plate_1.json without registering it).
func open3MF(inputPath string) (pathToUse string, cleanup func(), err error) {
	cleanup = func() {}
	pathToUse = inputPath

	zr, err := zip.OpenReader(inputPath)
	if err != nil {
		return "", cleanup, fmt.Errorf("open 3MF zip: %w", err)
	}
	defer zr.Close()

	var ctFile *zip.File
	for _, f := range zr.File {
		if f.Name == contentTypesName {
			ctFile = f
			break
		}
	}
	if ctFile == nil {
		return inputPath, cleanup, nil
	}

	rc, err := ctFile.Open()
	if err != nil {
		return "", cleanup, err
	}
	ctBytes, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return "", cleanup, err
	}

	var ct contentTypesXML
	if err := xml.Unmarshal(ctBytes, &ct); err != nil {
		return inputPath, cleanup, nil
	}

	defaults := make(map[string]string)
	overrides := make(map[string]string)
	for _, d := range ct.Default {
		ext := strings.ToLower(strings.TrimPrefix(d.Extension, "."))
		if ext != "" {
			defaults[ext] = d.ContentType
		}
	}
	for _, o := range ct.Override {
		partName := strings.ToUpper(normalizePartName(o.PartName))
		overrides[partName] = o.ContentType
	}

	// OPC part names in zip are without leading slash; findType uses normalized with slash.
	hasMissing := false
	for _, f := range zr.File {
		if f.Name == contentTypesName || strings.HasPrefix(f.Name, "_rels") || strings.HasSuffix(f.Name, "/") {
			continue
		}
		partName := "/" + f.Name
		norm := strings.ToUpper(normalizePartName(partName))
		if overrides[norm] != "" {
			continue
		}
		ext := path.Ext(partName)
		if ext != "" {
			extKey := strings.ToLower(strings.TrimPrefix(ext, "."))
			if defaults[extKey] != "" {
				continue
			}
		}
		hasMissing = true
		break
	}
	if !hasMissing {
		return inputPath, cleanup, nil
	}

	// Add content types for any part not listed in [Content_Types].xml.
	defaultsAdded := make(map[string]string)
	for _, f := range zr.File {
		if f.Name == contentTypesName || strings.HasPrefix(f.Name, "_rels") || strings.HasSuffix(f.Name, "/") {
			continue
		}
		partName := "/" + f.Name
		norm := strings.ToUpper(normalizePartName(partName))
		if overrides[norm] != "" {
			continue
		}
		ext := path.Ext(partName)
		if ext == "" {
			ct.Override = append(ct.Override, struct {
				PartName    string `xml:"PartName,attr"`
				ContentType string `xml:"ContentType,attr"`
			}{PartName: partName, ContentType: "application/octet-stream"})
			continue
		}
		extKey := strings.ToLower(strings.TrimPrefix(ext, "."))
		if defaults[extKey] != "" || defaultsAdded[extKey] != "" {
			continue
		}
		defaultsAdded[extKey] = extensionContentType(extKey)
		ct.Default = append(ct.Default, struct {
			Extension   string `xml:"Extension,attr"`
			ContentType string `xml:"ContentType,attr"`
		}{Extension: extKey, ContentType: defaultsAdded[extKey]})
	}

	patched, err := xml.MarshalIndent(&ct, "", "  ")
	if err != nil {
		return "", cleanup, err
	}
	patched = append([]byte(xml.Header), patched...)

	tmp, err := os.CreateTemp("", "3mf2stl-*.3mf")
	if err != nil {
		return "", cleanup, err
	}
	tmpPath := tmp.Name()
	cleanup = func() { os.Remove(tmpPath) }

	zw := zip.NewWriter(tmp)
	for _, f := range zr.File {
		var body io.Reader
		if f.Name == contentTypesName {
			body = bytes.NewReader(patched)
		} else {
			rc, err := f.Open()
			if err != nil {
				zw.Close()
				tmp.Close()
				cleanup()
				return "", func() {}, err
			}
			body = rc
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			zw.Close()
			tmp.Close()
			cleanup()
			return "", func() {}, err
		}
		if _, err := io.Copy(w, body); err != nil {
			zw.Close()
			tmp.Close()
			cleanup()
			return "", func() {}, err
		}
		if rc, ok := body.(io.Closer); ok {
			rc.Close()
		}
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmpPath, cleanup, nil
}

func normalizePartName(name string) string {
	name = path.Clean(name)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return strings.ReplaceAll(name, "\\", "/")
}

func extensionContentType(ext string) string {
	switch ext {
	case "json":
		return "application/json"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	default:
		return "application/octet-stream"
	}
}

func convert3MFToSTL(inputPath, outputPath string) error {
	pathToUse, cleanup, err := open3MF(inputPath)
	if err != nil {
		return err
	}
	defer cleanup()

	r, err := go3mf.OpenReader(pathToUse)
	if err != nil {
		return fmt.Errorf("open 3MF: %w", err)
	}
	defer r.Close()

	var model go3mf.Model
	if err := r.Decode(&model); err != nil {
		return fmt.Errorf("decode 3MF: %w", err)
	}

	solid := &stl.Solid{Name: "3mf2stl"}
	solid.SetASCII(false) // binary STL for visualizers

	identity := go3mf.Identity()
	for _, item := range model.Build.Items {
		obj, resPath := resolveObject(&model, item.ObjectPath(), item.ObjectID)
		if obj == nil {
			continue
		}
		collectTriangles(&model, resPath, obj, &item.Transform, solid)
	}

	// Fallback: some 3MFs have no build items or use a different structure; collect all meshes.
	if len(solid.Triangles) == 0 {
		_ = model.WalkObjects(func(path string, obj *go3mf.Object) error {
			if obj.Mesh != nil {
				collectTriangles(&model, path, obj, &identity, solid)
			}
			return nil
		})
	}

	if len(solid.Triangles) == 0 {
		return fmt.Errorf("no mesh data found in 3MF file")
	}

	return solid.WriteFile(outputPath)
}

// resolveObject finds an object by ID, trying path and common fallbacks (root "", PathOrDefault, model Path).
func resolveObject(m *go3mf.Model, path string, id uint32) (*go3mf.Object, string) {
	tryPaths := []string{path}
	if path != "" {
		tryPaths = append(tryPaths, "", m.PathOrDefault(), m.Path)
	}
	for _, p := range tryPaths {
		if obj, ok := m.FindObject(p, id); ok {
			return obj, p
		}
	}
	return nil, ""
}

func collectTriangles(m *go3mf.Model, resPath string, obj *go3mf.Object, transform *go3mf.Matrix, solid *stl.Solid) {
	if obj.Mesh != nil {
		meshToSTLTriangles(obj.Mesh, transform, solid)
		return
	}
	if obj.Components == nil {
		return
	}
	for _, comp := range obj.Components.Component {
		compPath := comp.ObjectPath(resPath)
		if compPath == "" {
			compPath = m.PathOrDefault()
		}
		child, childPath := resolveObject(m, compPath, comp.ObjectID)
		if child == nil {
			continue
		}
		combined := transform.Mul(comp.Transform)
		collectTriangles(m, childPath, child, &combined, solid)
	}
}

func meshToSTLTriangles(mesh *go3mf.Mesh, transform *go3mf.Matrix, solid *stl.Solid) {
	vertices := mesh.Vertices.Vertex
	triangles := mesh.Triangles.Triangle

	for i := range triangles {
		t := &triangles[i]
		// go3mf stores 0-based vertex indices (valid range 0..len(vertices)-1)
		v1Idx := int(t.V1)
		v2Idx := int(t.V2)
		v3Idx := int(t.V3)
		if v1Idx < 0 || v2Idx < 0 || v3Idx < 0 ||
			v1Idx >= len(vertices) || v2Idx >= len(vertices) || v3Idx >= len(vertices) {
			continue
		}

		p0 := transform.Mul3D(vertices[v1Idx])
		p1 := transform.Mul3D(vertices[v2Idx])
		p2 := transform.Mul3D(vertices[v3Idx])

		v0 := stl.Vec3{float32(p0.X()), float32(p0.Y()), float32(p0.Z())}
		v1 := stl.Vec3{float32(p1.X()), float32(p1.Y()), float32(p1.Z())}
		v2 := stl.Vec3{float32(p2.X()), float32(p2.Y()), float32(p2.Z())}

		normal := normalFromTriangle(v0, v1, v2)
		solid.AppendTriangle(stl.Triangle{
			Normal:     normal,
			Vertices:   [3]stl.Vec3{v0, v1, v2},
			Attributes: 0,
		})
	}
}

func normalFromTriangle(a, b, c stl.Vec3) stl.Vec3 {
	ab := stl.Vec3{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	ac := stl.Vec3{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	n := stl.Vec3{
		ab[1]*ac[2] - ab[2]*ac[1],
		ab[2]*ac[0] - ab[0]*ac[2],
		ab[0]*ac[1] - ab[1]*ac[0],
	}
	len := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
	if len == 0 {
		return stl.Vec3{0, 0, 0}
	}
	return stl.Vec3{n[0] / len, n[1] / len, n[2] / len}
}
