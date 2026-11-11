package format

import "testing"

// G++ syntax survives formatting, and formatting is idempotent.
func TestGppSyntax(t *testing.T) {
	tests := []struct{ in, want string }{
		{
			"package p\nfunc (c Circle)   Area() float64   =>   math.Pi*c.R*c.R\n",
			"package p\n\nfunc (c Circle) Area() float64 => math.Pi * c.R * c.R\n",
		},
		{
			"package p\nfunc f(s string) (int,error) => g(s)?*2,nil\nfunc h() => fmt.Println( 1 )\n",
			"package p\n\nfunc f(s string) (int, error) => g(s)? * 2, nil\nfunc h() => fmt.Println(1)\n",
		},
		{
			"package p\nvar x = a>0 ? a : -a\nvar y = c ?\n\t1 :\n\t2\n",
			"package p\n\nvar x = a > 0 ? a : -a\nvar y = c ?\n\t1 :\n\t2\n",
		},
		{
			"package p\nvar f = (a,b int)=>a+b\nvar g = x=>{\nreturn x\n}\nvar h = ()=>42\n",
			"package p\n\nvar f = (a, b int) => a + b\nvar g = x => {\n\treturn x\n}\nvar h = () => 42\n",
		},
		{
			"package p\nvar a = f()?   \"loading\"\nvar b = map[int]int{k()?: 1}\nvar c = xs[lo()?:]\nvar d = g()?  err=>err\n",
			"package p\n\nvar a = f()? \"loading\"\nvar b = map[int]int{k()?: 1}\nvar c = xs[lo()?:]\nvar d = g()? err => err\n",
		},
	}
	for _, tt := range tests {
		got, err := Source([]byte(tt.in))
		if err != nil {
			t.Errorf("%q: %v", tt.in, err)
			continue
		}
		if string(got) != tt.want {
			t.Errorf("formatting %q:\ngot:\n%s\nwant:\n%s", tt.in, got, tt.want)
		}
		again, err := Source(got)
		if err != nil || string(again) != string(got) {
			t.Errorf("not idempotent: %q -> %q (%v)", got, again, err)
		}
	}
}
