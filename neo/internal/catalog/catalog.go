// Package catalog 定义「卖什么」。
//
// 这里刻意只是一张静态表，而不是读数据库的后台页面：
// 售卖项是运营决策（定价、上架、场景描述），应当由人改代码+评审，
// 而不是让某个页面悄悄改掉。
//
// 产品形态：
//   - 云主机（含高级型）：算力主体，可单独申购
//   - GPU 型号：按型号上架，申购时才选张数；必须挂在云主机上
//
// 「数量只能在申请时选」这条是有意的：产品页回答的是「这是什么」，
// 「我要几张」是下单动作。混在一张卡里，页面就得同时承担选型和下单两件事。
package catalog

// Flavor 是云主机规格（算力主体）。
type Flavor struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	VCPU      int    `json:"vcpu"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGB    int    `json:"disk_gb"`
	Scene     string `json:"scene"`
	// Available 由当前容量实时算出，不是静态配置
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// GPUModel 是按型号上架的显卡。
//
// 页面上只展示型号；申购张数在申请表单里选 —— 选型和定量是两件事。
type GPUModel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// VRAMGB 是单卡显存
	VRAMGB int    `json:"vram_gb"`
	Scene  string `json:"scene"`
	Desc   string `json:"desc"`
	// Total 是本机该型号的插槽数；0 表示这台机器上没有这个型号
	Total int `json:"total"`
	// RequiresFlavor 恒为 true：GPU 不能脱离云主机独立售卖
	RequiresFlavor bool `json:"requires_flavor"`
	// MatchPCI 用于把实例上已挂的卡认回型号（内容来自设备的 product/id）。
	// 留空的型号不参与匹配，也就不会因为认不出来而被误算成已占用。
	MatchPCI string `json:"-"`
	// DefaultTarget 表示「认不出型号的已用卡全部记到它头上」。
	// 宁可少卖一张，也不能算错以后超卖。
	DefaultTarget bool `json:"-"`
	// Available / AvailableCards / Reason 由 EvaluateGPUs 实时算出。
	// AvailableCards 是「当前最多还能配几张」，前端的数量选择器直接按它渲染，
	// 不必自己从总数推导（推导逻辑只在服务端有一份）。
	Available      bool   `json:"available"`
	AvailableCards int    `json:"available_cards"`
	Reason         string `json:"reason,omitempty"`
}

// MaxGPUCards 是单个实例可配的显卡上限（不再支持混合型号）。
const MaxGPUCards = 2

// Image 是可选的系统镜像。
type Image struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	Title     string `json:"title"`
	Desc      string `json:"desc"`
	Available bool   `json:"available"`
	// Default 为 true 时作为申请单的默认值，前端不必再让用户选
	Default bool `json:"default"`
}

// LoginUser 是所有实例的默认登录账户。
const LoginUser = "root"

// Catalog 是整份目录。
type Catalog struct {
	Flavors    []Flavor
	GPUModels  []GPUModel
	Images     []Image
}

// Default 返回当前上架的目录。
//
// 两张 4090 是实测结果（nvidia-smi -L）；RTX PRO 6000 这台机器上没有，
// 所以它的库存是 0，对外表述为「待上架」——
// 这是商品口径（还没上货架），不是机器口径（本机没装）。
func Default() Catalog {
	return Catalog{
		Flavors: []Flavor{
			{
				Name: "s2.m4.d10", Title: "入门型", VCPU: 2, MemoryMiB: 4096, DiskGB: 10,
				Scene: "Web 服务、个人开发机、长期跑的小任务",
			},
			{
				Name: "s4.m8.d20", Title: "通用型", VCPU: 4, MemoryMiB: 8192, DiskGB: 20,
				Scene: "大型项目编译、多服务联调、轻量数据库",
			},
		},
		GPUModels: []GPUModel{
			{
				ID: "rtx-4090", Title: "RTX 4090", VRAMGB: 24, Total: 1,
				Scene: "单卡推理、LoRA 微调、CUDA 课程实验",
				Desc:  "消费级旗舰，24GB 显存，整机共 1 张",
				// Device 2684 由 lspci 实测得到，用来把已挂的卡认回这个型号
				MatchPCI:      "2684",
				DefaultTarget: true,
				RequiresFlavor: true,
			},
			{
				ID: "rtx-pro-6000", Title: "RTX PRO 6000", VRAMGB: 96, Total: 0,
				Scene: "大模型训练、95GB 级显存的推理场景",
				Desc:  "专业卡，96GB 显存，待上架",
				RequiresFlavor: true,
			},
		},
		Images: []Image{
			{
				ID: "ubuntu-22.04-base", Alias: "images:ubuntu/22.04", Available: true, Default: true,
				Title: "Ubuntu 22.04 LTS",
				Desc:  "与现有实例同源，已在本机验证可跑；首次创建需联网拉取镜像，通常 2~5 分钟",
			},
			{
				ID: "ubuntu-24.04-base", Alias: "images:ubuntu/24.04", Available: true,
				Title: "Ubuntu 24.04 LTS",
				Desc:  "较新的工具链，同样首次需联网拉取",
			},
			{
				ID: "ubuntu-22.04-cuda", Alias: "local/ubuntu-22.04-cuda", Available: false,
				Title: "Ubuntu 22.04 + CUDA 12.4（筹备中）",
				Desc:  "需先把驱动与 CUDA 做成离线缓存镜像，暂不可选",
			},
		},
	}
}

// WithGPUStock 接受一次实测库存，覆盖标注了 DefaultTarget 的型号。
//
// 为什么要留这个口子：resources 接口上报不到显卡，宿主换卡、拔卡之后
// 不该要求改代码重新发版，运维在 compose 里改个数就行。
// 只对 DefaultTarget 生效（即本机实有的那一张），不会把没装的卡「配置出来」。
func (c Catalog) WithGPUStock(n int) Catalog {
	out := c
	for i := range out.GPUModels {
		if out.GPUModels[i].DefaultTarget && n > 0 {
			out.GPUModels[i].Total = n
		}
	}
	return out
}

// Capacity 是当前宿主占用，用于判断某项是否还能交付。
// 各 Total<=0 表示这一项未知（例如取不到 resources），此时不做减法。
type Capacity struct {
	CPUTotal        int
	CPUAllocated    int
	MemoryTotal     int64
	MemoryAllocated int64
	DiskTotal       int64
	DiskAllocated   int64
	// GPUAllocatedByModel 是按型号统计的已占卡数；
	// 识别不出型号的部分由调用方归到 DefaultTarget 名下
	GPUAllocatedByModel map[string]int
}

// EvaluateFlavors 按当前容量标注每个云主机规格的可售状态，返回副本，不改原表。
func EvaluateFlavors(flavors []Flavor, c Capacity) []Flavor {
	out := make([]Flavor, 0, len(flavors))
	for _, f := range flavors {
		f.Available = true
		f.Reason = ""

		if c.CPUTotal > 0 && c.CPUAllocated+f.VCPU > c.CPUTotal {
			f.Available = false
			f.Reason = "宿主 CPU 余量不足"
		}
		need := int64(f.MemoryMiB) * 1024 * 1024
		if f.Available && c.MemoryTotal > 0 && c.MemoryAllocated+need > c.MemoryTotal {
			f.Available = false
			f.Reason = "宿主内存余量不足"
		}
		if f.Available && c.DiskTotal > 0 && c.DiskAllocated+int64(f.DiskGB)*1024*1024*1024 > c.DiskTotal {
			f.Available = false
			f.Reason = "宿主磁盘余量不足"
		}
		out = append(out, f)
	}
	return out
}

// EvaluateGPUs 按每种型号的剩余库存标注可售状态。
//
// 库存为 0 的型号一律标「待上架」，已上过架但被占满的标「已被全部占用」——
// 「还没上」和「已经用完」对申请人是完全不同的信息，不能共用一个说法。
// 注意这里谈的是**库存**（能卖多少），不是机器上插了几张卡。
func EvaluateGPUs(models []GPUModel, c Capacity) []GPUModel {
	out := make([]GPUModel, 0, len(models))
	for _, m := range models {
		m.Available = true
		m.Reason = ""

		used := c.GPUAllocatedByModel[m.ID]
		switch {
		case m.Total <= 0:
			m.Available = false
			m.Reason = "待上架"
		case m.Total-used <= 0:
			m.Available = false
			m.Reason = "已被全部占用"
		}
		m.AvailableCards = MaxCardsFor(m, used)
		out = append(out, m)
	}
	return out
}

// MaxCardsFor 返回某个型号当前最多能配几张，同时受实例上限与剩余库存约束。
// used 为该型号已被占用的张数。
func MaxCardsFor(m GPUModel, used int) int {
	if m.Total <= 0 {
		return 0
	}
	n := m.Total - used
	if n < 0 {
		return 0
	}
	if n > MaxGPUCards {
		return MaxGPUCards
	}
	return n
}

// Find 按名字查云主机规格。
func (c Catalog) Find(name string) (Flavor, bool) {
	for _, f := range c.Flavors {
		if f.Name == name {
			return f, true
		}
	}
	return Flavor{}, false
}

// FindGPU 按型号 ID 查显卡。空字符串表示不配卡，是合法取值。
func (c Catalog) FindGPU(id string) (GPUModel, bool) {
	if id == "" {
		return GPUModel{}, true
	}
	for _, m := range c.GPUModels {
		if m.ID == id {
			return m, true
		}
	}
	return GPUModel{}, false
}

// DefaultGPUTarget 返回承载「认不出型号」的那一型，调用方据此归集未知占用。
func (c Catalog) DefaultGPUTarget() (GPUModel, bool) {
	for _, m := range c.GPUModels {
		if m.DefaultTarget {
			return m, true
		}
	}
	return GPUModel{}, false
}

// FindImage 按 ID 查镜像，只返回可售的。
func (c Catalog) FindImage(id string) (Image, bool) {
	for _, i := range c.Images {
		if i.ID == id && i.Available {
			return i, true
		}
	}
	return Image{}, false
}

// DefaultImage 返回默认镜像。
//
// 第一版不让用户在申请单选镜像：选镜像是个技术判断，
// 而大多数申请人只想要一台能 ssh 上去的机器。
func (c Catalog) DefaultImage() (Image, bool) {
	for _, i := range c.Images {
		if i.Default && i.Available {
			return i, true
		}
	}
	return c.findFirstAvailableImage()
}

func (c Catalog) findFirstAvailableImage() (Image, bool) {
	for _, i := range c.Images {
		if i.Available {
			return i, true
		}
	}
	return Image{}, false
}
