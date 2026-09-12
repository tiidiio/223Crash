package main
import ("fmt"; "math/rand"; "time")
func main(){
 rand.Seed(time.Now().UnixNano())
 r:=1
 for{
  crash:=1.10+rand.Float64()*15
  if rand.Float64()<0.1 { crash=1.05+rand.Float64()*0.4 }
  m:=1.00
  for m<crash{
   fmt.Printf("\r\033[K Rund: %d | crash point : %.2f Multiplicateur : %.2f.....%.2f", r, crash, m, crash)
   time.Sleep(80*time.Millisecond)
   m+=0.05
   if m>crash { m=crash }
  }
  fmt.Printf("\r\033[K Rund: %d | crash point : %.2f Multiplicateur : %.2f.....%.2f 💥", r, crash, crash, crash)
  time.Sleep(700*time.Millisecond)
  r++
 }
}
