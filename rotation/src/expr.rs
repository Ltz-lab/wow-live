//! Tiny condition language for rotations, e.g.
//!   holy_power>=3&!buff.avenging_wrath.up|cooldown.wake_of_ashes.remains>6
//!   pet.wild_imp.active>=6
//! Compiled once to an AST with names resolved to indices.

#[derive(Debug, Clone)]
pub enum Var {
    Resource(usize),
    ResourceMax(usize),
    ResourcePct(usize),
    Mastery,
    Haste,
    /// The player's current crit chance (rating plus crit buffs).
    Crit,
    GcdRemains,
    PrevGcd(usize),
    /// What the player's last cast paid of a resource (`spent.<resource>`).
    Spent(usize),
    Gcd,
    Time,
    FightRemains,
    HealthPct,
    CdReady(usize),
    CdRemains(usize),
    CdCharges(usize),
    CdChargesFrac(usize),
    /// Current (hasted) cooldown length.
    CdDuration(usize),
    /// Time until every charge is back (`full_recharge_time`).
    CdFullRecharge(usize),
    BuffUp(usize),
    BuffRemains(usize),
    BuffStack(usize),
    DotTicking(usize),
    DotRemains(usize),
    DotStack(usize),
    /// Active instances of a pet.
    PetActive(usize),
    /// Longest time left among a pet's active instances.
    PetRemains(usize),
    /// The pet instance an effect or pet action runs for (`self.*`).
    SelfEnergy,
    SelfRemains,
    /// Enemies in range (the sim is single target and resolves `active_enemies` to a constant;
    /// the live overlay counts them).
    ActiveEnemies,
    Const(f64),
}

#[derive(Debug, Clone)]
pub enum Expr {
    Num(f64),
    Var(Var),
    Not(Box<Expr>),
    Neg(Box<Expr>),
    Bin(Op, Box<Expr>, Box<Expr>),
}

#[derive(Debug, Clone, Copy)]
pub enum Op { Or, And, Lt, Le, Gt, Ge, Eq, Ne, Add, Sub, Mul, Div }

pub trait Resolver {
    fn resolve(&self, name: &str) -> Result<Var, String>;
}

pub trait Env {
    fn var(&self, v: &Var) -> f64;
}

impl Expr {
    pub fn eval<E: Env>(&self, env: &E) -> f64 {
        match self {
            Expr::Num(n) => *n,
            Expr::Var(v) => env.var(v),
            Expr::Not(e) => b(e.eval(env) == 0.0),
            Expr::Neg(e) => -e.eval(env),
            Expr::Bin(op, l, r) => {
                // short-circuit logic ops
                match op {
                    Op::Or => return b(l.eval(env) != 0.0 || r.eval(env) != 0.0),
                    Op::And => return b(l.eval(env) != 0.0 && r.eval(env) != 0.0),
                    _ => {}
                }
                let (a, c) = (l.eval(env), r.eval(env));
                match op {
                    Op::Lt => b(a < c),
                    Op::Le => b(a <= c),
                    Op::Gt => b(a > c),
                    Op::Ge => b(a >= c),
                    Op::Eq => b((a - c).abs() < 1e-9),
                    Op::Ne => b((a - c).abs() >= 1e-9),
                    Op::Add => a + c,
                    Op::Sub => a - c,
                    Op::Mul => a * c,
                    Op::Div => if c == 0.0 { 0.0 } else { a / c },
                    Op::Or | Op::And => unreachable!(),
                }
            }
        }
    }
}

fn b(x: bool) -> f64 { if x { 1.0 } else { 0.0 } }

#[derive(Debug, Clone, PartialEq)]
enum Tok { Num(f64), Ident(String), Op(&'static str), LParen, RParen }

fn lex(s: &str) -> Result<Vec<Tok>, String> {
    let c: Vec<char> = s.chars().collect();
    let mut i = 0;
    let mut out = Vec::new();
    while i < c.len() {
        let ch = c[i];
        if ch.is_whitespace() { i += 1; continue; }
        if ch.is_ascii_digit() || (ch == '.' && i + 1 < c.len() && c[i + 1].is_ascii_digit()) {
            let st = i;
            while i < c.len() && (c[i].is_ascii_digit() || c[i] == '.') { i += 1; }
            let t: String = c[st..i].iter().collect();
            out.push(Tok::Num(t.parse().map_err(|_| format!("bad number {t}"))?));
            continue;
        }
        if ch.is_ascii_alphabetic() || ch == '_' {
            let st = i;
            while i < c.len() && (c[i].is_ascii_alphanumeric() || c[i] == '_' || c[i] == '.') { i += 1; }
            out.push(Tok::Ident(c[st..i].iter().collect()));
            continue;
        }
        let two: String = c[i..(i + 2).min(c.len())].iter().collect();
        let op2 = ["<=", ">=", "!=", "=="].iter().find(|o| **o == two);
        if let Some(o) = op2 { out.push(Tok::Op(o)); i += 2; continue; }
        let op1 = match ch {
            '|' => "|", '&' => "&", '!' => "!", '<' => "<", '>' => ">", '=' => "=",
            '+' => "+", '-' => "-", '*' => "*", '/' => "/", '%' => "/",
            '(' => { out.push(Tok::LParen); i += 1; continue; }
            ')' => { out.push(Tok::RParen); i += 1; continue; }
            _ => return Err(format!("unexpected '{ch}' in '{s}'")),
        };
        out.push(Tok::Op(op1));
        i += 1;
    }
    Ok(out)
}

struct P<'a, R: Resolver> { t: Vec<Tok>, i: usize, r: &'a R }

fn prec(op: &str) -> Option<(u8, Op)> {
    Some(match op {
        "|" => (1, Op::Or),
        "&" => (2, Op::And),
        "<" => (3, Op::Lt), "<=" => (3, Op::Le), ">" => (3, Op::Gt), ">=" => (3, Op::Ge),
        "=" | "==" => (3, Op::Eq), "!=" => (3, Op::Ne),
        "+" => (4, Op::Add), "-" => (4, Op::Sub),
        "*" => (5, Op::Mul), "/" => (5, Op::Div),
        _ => return None,
    })
}

impl<'a, R: Resolver> P<'a, R> {
    fn peek(&self) -> Option<&Tok> { self.t.get(self.i) }
    fn next(&mut self) -> Option<Tok> { let t = self.t.get(self.i).cloned(); self.i += 1; t }

    fn expr(&mut self, min: u8) -> Result<Expr, String> {
        let mut lhs = self.unary()?;
        loop {
            let op = match self.peek() { Some(Tok::Op(o)) => *o, _ => break };
            let Some((p, o)) = prec(op) else { break };
            if p < min { break; }
            self.i += 1;
            let rhs = self.expr(p + 1)?;
            lhs = Expr::Bin(o, Box::new(lhs), Box::new(rhs));
        }
        Ok(lhs)
    }

    fn unary(&mut self) -> Result<Expr, String> {
        match self.next() {
            Some(Tok::Op("!")) => Ok(Expr::Not(Box::new(self.unary()?))),
            Some(Tok::Op("-")) => Ok(Expr::Neg(Box::new(self.unary()?))),
            Some(Tok::Num(n)) => Ok(Expr::Num(n)),
            Some(Tok::Ident(s)) => Ok(Expr::Var(self.r.resolve(&s)?)),
            Some(Tok::LParen) => {
                let e = self.expr(0)?;
                match self.next() { Some(Tok::RParen) => Ok(e), _ => Err("missing ')'".into()) }
            }
            t => Err(format!("unexpected token {t:?}")),
        }
    }
}

pub fn parse<R: Resolver>(s: &str, r: &R) -> Result<Expr, String> {
    let mut p = P { t: lex(s)?, i: 0, r };
    let e = p.expr(0)?;
    if p.i != p.t.len() { return Err(format!("trailing tokens in '{s}'")); }
    Ok(e)
}
