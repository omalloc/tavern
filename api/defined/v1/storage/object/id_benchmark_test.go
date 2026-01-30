package object

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/cespare/xxhash/v2"
)

// Test data simulating typical CDN cache key inputs
var testCacheKeys = []struct {
	name string
	key  string
}{
	{"short", "/images/logo.png"},
	{"medium", "/api/v1/users/12345/profile/avatar.jpg?size=200&format=webp"},
	{"long", "/content/2026/01/30/articles/how-to-build-a-high-performance-cdn-cache-system-with-go/featured-image.png?w=1920&h=1080&quality=85&format=avif"},
	{"with_query_params", "/static/js/app.bundle.js?v=1.2.3&hash=abc123def456&timestamp=1706630400"},
	{"complex_path", "/bucket/region-cn-east-1/tenant-12345/project-67890/assets/uploads/2026/01/file-with-special-chars_v2.1.0-beta.tar.gz"},
}

// ============================================================================
// xxHash64 Benchmarks
// ============================================================================

func BenchmarkXXHash64_Short(b *testing.B) {
	key := testCacheKeys[0].key
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xxhash.Sum64String(key)
	}
}

func BenchmarkXXHash64_Medium(b *testing.B) {
	key := testCacheKeys[1].key
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xxhash.Sum64String(key)
	}
}

func BenchmarkXXHash64_Long(b *testing.B) {
	key := testCacheKeys[2].key
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xxhash.Sum64String(key)
	}
}

func BenchmarkXXHash64_WithQueryParams(b *testing.B) {
	key := testCacheKeys[3].key
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xxhash.Sum64String(key)
	}
}

func BenchmarkXXHash64_ComplexPath(b *testing.B) {
	key := testCacheKeys[4].key
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xxhash.Sum64String(key)
	}
}

// ============================================================================
// MD5 Benchmarks
// ============================================================================

func BenchmarkMD5_Short(b *testing.B) {
	key := []byte(testCacheKeys[0].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = md5.Sum(key)
	}
}

func BenchmarkMD5_Medium(b *testing.B) {
	key := []byte(testCacheKeys[1].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = md5.Sum(key)
	}
}

func BenchmarkMD5_Long(b *testing.B) {
	key := []byte(testCacheKeys[2].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = md5.Sum(key)
	}
}

func BenchmarkMD5_WithQueryParams(b *testing.B) {
	key := []byte(testCacheKeys[3].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = md5.Sum(key)
	}
}

func BenchmarkMD5_ComplexPath(b *testing.B) {
	key := []byte(testCacheKeys[4].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = md5.Sum(key)
	}
}

// ============================================================================
// SHA1 Benchmarks
// ============================================================================

func BenchmarkSHA1_Short(b *testing.B) {
	key := []byte(testCacheKeys[0].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sha1.Sum(key)
	}
}

func BenchmarkSHA1_Medium(b *testing.B) {
	key := []byte(testCacheKeys[1].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sha1.Sum(key)
	}
}

func BenchmarkSHA1_Long(b *testing.B) {
	key := []byte(testCacheKeys[2].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sha1.Sum(key)
	}
}

func BenchmarkSHA1_WithQueryParams(b *testing.B) {
	key := []byte(testCacheKeys[3].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sha1.Sum(key)
	}
}

func BenchmarkSHA1_ComplexPath(b *testing.B) {
	key := []byte(testCacheKeys[4].key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sha1.Sum(key)
	}
}

// ============================================================================
// Full CacheKey Generation Benchmarks (Hash + Hex Encoding)
// ============================================================================

func BenchmarkCacheKeyGen_XXHash64(b *testing.B) {
	for _, tc := range testCacheKeys {
		b.Run(tc.name, func(b *testing.B) {
			key := tc.key
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h := xxhash.Sum64String(key)
				_ = fmt.Sprintf("%016x", h)
			}
		})
	}
}

func BenchmarkCacheKeyGen_MD5(b *testing.B) {
	for _, tc := range testCacheKeys {
		b.Run(tc.name, func(b *testing.B) {
			key := []byte(tc.key)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h := md5.Sum(key)
				_ = hex.EncodeToString(h[:])
			}
		})
	}
}

func BenchmarkCacheKeyGen_SHA1(b *testing.B) {
	for _, tc := range testCacheKeys {
		b.Run(tc.name, func(b *testing.B) {
			key := []byte(tc.key)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h := sha1.Sum(key)
				_ = hex.EncodeToString(h[:])
			}
		})
	}
}

// ============================================================================
// Parallel Benchmarks (simulating concurrent cache key generation)
// ============================================================================

func BenchmarkParallel_XXHash64(b *testing.B) {
	key := testCacheKeys[2].key // use long key
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = xxhash.Sum64String(key)
		}
	})
}

func BenchmarkParallel_MD5(b *testing.B) {
	key := []byte(testCacheKeys[2].key) // use long key
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = md5.Sum(key)
		}
	})
}

func BenchmarkParallel_SHA1(b *testing.B) {
	key := []byte(testCacheKeys[2].key) // use long key
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = sha1.Sum(key)
		}
	})
}

// ============================================================================
// Memory Allocation Benchmarks
// ============================================================================

func BenchmarkAlloc_XXHash64(b *testing.B) {
	key := testCacheKeys[2].key
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := xxhash.Sum64String(key)
		_ = fmt.Sprintf("%016x", h)
	}
}

func BenchmarkAlloc_MD5(b *testing.B) {
	key := []byte(testCacheKeys[2].key)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := md5.Sum(key)
		_ = hex.EncodeToString(h[:])
	}
}

func BenchmarkAlloc_SHA1(b *testing.B) {
	key := []byte(testCacheKeys[2].key)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := sha1.Sum(key)
		_ = hex.EncodeToString(h[:])
	}
}
