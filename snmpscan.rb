#!/usr/bin/env ruby

require 'rubygems'
require 'snmp'
require 'yaml'

class Integer
    def byte_to_Mbit
        return (self * 8 / 1024 / 1024) 
    end
end

TIOCGWINSZ = 0x5413
page_time = 1 

def get_console_cols_rows

    cols = 130
    rows = 40
    begin
        buf = [0, 0, 0, 0].pack("SSSS")
        if STDOUT.ioctl(TIOCGWINSZ, buf) >= 0 then
            rows, cols, row_pixels, row_pixels, col_pixels = buf.unpack("SSSS")[0..1]
        end
    rescue
    end
    return cols,rows

end


# Function for help

def show_help
    puts "                                                                   "
    puts "  #{$0} -c <community> -h <Host> [options]                         "
    puts "                                                                   "
    puts "  SNMPSCAN version 1.6.3                                           "
    puts "                                                                   "
    puts "  -h            IP-address or the Hostname of the Targetsystem     "
    puts "  -c            SNMP community                                     "
    puts "  -r            specify the name of displayed Interfaces           "
    puts "  -m            Mark line of selected Interface IP                 "
    puts "  -u            disable additional System-Infos                    "
    puts "                                                                   "
    puts "  --help        display this help and exit                         "
    puts "  --version     output version information and exit                "
    puts "                                                                   "
    exit
end

# Function for version

def show_version
    puts "                                                                   "
    puts "  SNMPSCAN version 1.6.3                                           "
    puts "                                                                   "
end

r_opt = []    # Array for interface filter reg exp
h_opt = nil   # Host
c_opt = nil   # Comunity
m_opt = "no"  # default for line mark
u_opt = true  # default show addition information

reg_ip = Regexp.new '\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b'
reg_ipv6 = /^\s*((([0-9A-Fa-f]{1,4}:){7}([0-9A-Fa-f]{1,4}|:))|(([0-9A-Fa-f]{1,4}:){6}(:[0-9A-Fa-f]{1,4}|((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3})|:))|(([0-9A-Fa-f]{1,4}:){5}(((:[0-9A-Fa-f]{1,4}){1,2})|:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3})|:))|(([0-9A-Fa-f]{1,4}:){4}(((:[0-9A-Fa-f]{1,4}){1,3})|((:[0-9A-Fa-f]{1,4})?:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9A-Fa-f]{1,4}:){3}(((:[0-9A-Fa-f]{1,4}){1,4})|((:[0-9A-Fa-f]{1,4}){0,2}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9A-Fa-f]{1,4}:){2}(((:[0-9A-Fa-f]{1,4}){1,5})|((:[0-9A-Fa-f]{1,4}){0,3}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9A-Fa-f]{1,4}:){1}(((:[0-9A-Fa-f]{1,4}){1,6})|((:[0-9A-Fa-f]{1,4}){0,4}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(:(((:[0-9A-Fa-f]{1,4}){1,7})|((:[0-9A-Fa-f]{1,4}){0,5}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:)))(%.+)?\s*$/ 

reg_domain = Regexp.new '^[A-Za-z0-9.-]+\.[A-Za-z]{2,5}$'

ARGV.each_with_index do |option , x |

    # check if version sould be printed

    if option == "--version" || option == "-v"
        show_version()
        exit
    end

    # check if help sould be printed

    if option == "--help"
        show_help()
        exit
    end

    #set host  identifier

    if option == "-h"
        if h_opt == nil

            # test if given host an IP or Domian

            if !(ARGV[x+1] =~ reg_ip) && !(ARGV[x+1] =~ reg_domain ) && !(ARGV[x+1] =~ reg_ipv6)
                print "\n Wrong IP format\n"
                show_help()
            else
                if RUBY_VERSION.to_f < 1.9 && ARGV[x+1] =~ reg_ipv6
                    print "\n Your Ruby-Version does not support IPv6\n"
                    show_help()
                else
                    h_opt = ARGV[x+1]
                end
            end
        else
            print "\n Multiple Hosts defined\n"
            show_help()
        end
    end

    #get Comunity

    if option == "-c"
        if c_opt == nil
            c_opt = ARGV[x+1]
        else
            print "\n Multiple Comunitys defined\n"
            show_help()
        end
    end

    #read inferface filter 

    if option == "-r"
        r_opt.push  ARGV[x+1]
        ARGV[x+1]=""
    end

    #mark of L3 interfaces

    if option == "-m"
        m_opt="yes"
    end

    #view additional information

    if option == "-u"
        u_opt=false
    end
end

# show help if host r community missing 
if h_opt == nil
    show_help()
end
if c_opt == nil
    show_help()
end

# definition of requesting fieldsl

iftable_columns = ["ifIndex", "ifDescr", "ifHCInOctets", "ifHCOutOctets", "ifInUcastPkts", "ifOutUcastPkts", "ifAlias", "ifInErrors"]

# clear scrren at control-c (don't show ruby errors)

trap('INT') do
    print "\e[1G" # jump to the start of the line
    100.times {print "\e[B"} # print 100 linebreaks (with disabled scrolling)
    exit
end

puts "\e[39m\e[2J" #  set default colour and clear screen
print "\e[1;1H\n" # jump to position 1:1

devs_config = [ 
    { 
        :name =>     '.*',
        :prio =>     0,
        :cpu_oid =>  "1.3.6.1.2.1.1.4",
        :default_filter => [ "" ]
    }
]

# load device config from .device files

[ ".snmpscan/" , "/etc/snmpscan/" , "~/.snmpscan/" ].each do |folder|
    Dir["#{folder}*.device"].each do |file|
        config = YAML.load_file(file)
        if config == false
            raise "error loading file #{file}"
        end
        devs_config = devs_config + config
    end
end

devs_config.sort! do |a , b|
    if a[:prio] == b[:prio] 
        a[:name] <=> b[:name] 
    else 
        a[:prio] <=> b[:prio] 
    end
end


# SNMP connect

begin
    SNMP::Manager.open(:Host => h_opt , :Community => c_opt , :Timeout => 1 , :Retries => 600) do |manager| 
        
        #get device-name to define additional information

        cpu_oid = ""
        filter = []
        add_infos = []

        systype = manager.get_value("1.3.6.1.2.1.1.1.0")

        devs_config.each do |dev|
            if systype =~ Regexp.new(dev[:name])
                cpu_oid = dev[:cpu_oid] if dev[:cpu_oid]
                filter = filter + dev[:default_filter] if dev[:default_filter] 
                add_infos = add_infos + dev[:add_infos] if dev[:add_infos] 
            end
        end
        # use filter of device-devs if no r_opt given
        if r_opt.length == 0
            r_opt =  filter
        end

        if u_opt ==  false 
            add_infos =  []
        end

        #search for interface with hostIP if m_opt given. write interfaceID to m_opt 

        if m_opt == "yes"
            manager.walk(["1.3.6.1.2.1.4.20.1.2 ","1.3.6.1.2.1.4.20.1.1"]) do |row|
                if row[1].value.to_s == h_opt
                    m_opt = row[0].value.to_i
                end
            end
        end


        reg_interface = r_opt.map { |r| Regexp.new(r)  }
        old = []           
        add_oid = []      

        time1 = nil

        #get oid for add_infos

        add_infos.each do |add_info|
            add_oid << add_info[:oid]
        end


        while 1

            print "\e[1;1H\e[K\n" #jump to 1:1 and clear first line

            #print default-data and CPU

            print " System: #{h_opt}       "                                          #print SystemName/IP
            print " Sysname: #{manager.get_value('1.3.6.1.2.1.1.5.0')}       "        #printsysnames
            print " CPU: "

            #print CPU

            manager.walk(cpu_oid) do |row|
                row.each do |vb|
                    print "#{vb.value} "  #.rjust(4)
                end
            end

            # check if last lopp is less then 10 seconds in the past. 

            if time1 != nil
                print "       Reload:"
                time2 = Time.now.to_i
                interval= time2 - time1
                STDOUT.flush
                if interval < 10
                    "#{10-interval}".to_i.downto(1) do |count|
                        print "#{count.to_s}  ".rjust(5)
                        print "\e[K"
                        STDOUT.flush
                        sleep(1)
                        print "\e[D\e[D\e[D\e[D\e[D"
                    end
                    interval= 10
                    time2 = Time.now.to_i
                end

            end

            time1 = Time.now.to_i

            print "\n\e[K\n\e[K"

            #get add_info

            add_value=manager.get_value(add_oid)

            add_value.each_with_index do |value, index|

                add_info =  add_infos[index]

                #check the result of the add_infos

                case add_infos[index][:type]
                when /same/

                    add_info[:relation].each_with_index do |relation|
                        add_reg = Regexp.new(relation[:test])
                        if  value.to_s =~ add_reg

                            print "\e[31m" if relation[:error] == "1"
                            print " #{add_info[:name]}".ljust(30)
                            print " #{relation[:output]}".ljust(30)
                            print "\e[K\n"
                            print "\e[39m" if relation[:error] == "1"

                        end
                    end

                when /max/

                    print "\e[31m" if add_info[:relation].to_i < value.to_i
                    print " #{add_info[:name]}".ljust(30)
                    print " #{value}".ljust(30)
                    print "\e[K\n"
                    print "\e[39m" if add_info[:relation].to_i < value.to_i

                when /min/

                    print "\e[31m" if add_info[:relation].to_i > value.to_i
                    print " #{add_info[:name]}".ljust(30)
                    print " #{value}".ljust(30)
                    print "\e[K\n"
                    print "\e[39m" if add_info[:relation].to_i > value.to_i

                end
            end

            cols , rows = get_console_cols_rows

            #printheader
            print "\e[K"
            print "\n ifNr      Port                                Incoming       Outgoing      Packets IN    Packets OUT   Errors    Alias\e[K\n "
            "#{cols-2}".to_i.times{print "-"}
            print "\n\e[K\n"

            STDOUT.flush

            #get interface counter
            paged = false
            printed_lines = add_infos.length + 7

            manager.walk(iftable_columns) do |row|

                #test if Interfacedescription or interfacename match on of regexp
                match=false
                reg_interface.each do |x|
                    if row[1].value =~ x || row[6].value =~ x
                        match=true
                        break
                    end
                end
                if match

                    cols,rows = get_console_cols_rows

                    int_id = row[0].value.to_i()

                    diffio = nil
                    diffoo =  nil 
                    diffip = nil
                    diffop =  nil
                    diff_err_in = nil

                    if old[int_id] != nil   

                        diffio =      (row[2].value.to_i - old[int_id][:ifHCInOctets].to_i  ) / interval
                        diffoo =      (row[3].value.to_i - old[int_id][:ifHCOutOctets].to_i ) / interval 
                        diffip =      (row[4].value.to_i - old[int_id][:ifInUcastPkts].to_i ) / interval
                        diffop =      (row[5].value.to_i - old[int_id][:ifOutUcastPkts].to_i) / interval
                        diff_err_in = (row[7].value.to_i - old[int_id][:ifInErrors].to_i    ) / interval

                        print "\e[1;30m"  if diffio.byte_to_Mbit < 5   && diffoo.byte_to_Mbit < 5   && diffip < 100    && diffop < 100 
                        print "\e[1;31m"  if diffio.byte_to_Mbit > 700 || diffoo.byte_to_Mbit > 700 || diffip > 100000 || diffop > 100000 || ( diff_err_in != 0)
                        print "\e[33m"    if m_opt == row[0].value.to_i

                    end
                    
                    print " #{row[0].value}".ljust(11)
                    print "#{row[1].value[0,30]}".ljust(30)

                    if old[int_id] != nil   
                        print "#{diffio.byte_to_Mbit} Mbit/s".rjust(15)
                        print "#{diffoo.byte_to_Mbit} Mbit/s".rjust(15)
                        print "#{diffip} pps".rjust(15)
                        print "#{diffop} pps".rjust(15)
                        print "#{diff_err_in} ".rjust(10) 
                    else
                        print " - ".rjust(15)
                        print " - ".rjust(15)
                        print " - ".rjust(15)
                        print " - ".rjust(15)
                        print " - ".rjust(10)
                    end 

                    print "   #{row[6].value[0,cols-115]}\e[K"
                    print "\n"

                    print "\e[0;39m" 

                    #save current values to old array
                    old[ int_id ] = { 
                        :ifIndex => "#{row[0].value}", 
                        :ifDescr => "#{row[1].value}", 
                        :ifHCInOctets => "#{row[2].value}", 
                        :ifHCOutOctets => "#{row[3].value}", 
                        :ifInUcastPkts => "#{row[4].value}", 
                        :ifOutUcastPkts => "#{row[5].value}", 
                        :ifAlias => "#{row[6].value}", 
                        :ifInErrors => "#{row[7].value}",
                    }
                    
                    printed_lines += 1
                    if printed_lines == rows - 1 
                        sleep page_time
                        paged = true
                        printed_lines = add_infos.length + 7
                        print "\e[#{printed_lines};1H\n" # jump to the start of the list
                    end
                end
            end
            if paged == false
                100.times {print "\e[2K\e[B"} # clear rest of screen if not paged
            else
                sleep page_time
            end
        end
    end
rescue SNMP::RequestTimeout

    print "\e[1G" # an den Anfang der zeile springen um in die erste Spalte zu kommen
    100.times {print "\e[B"} # ans ende vom Terminal springen
    puts "Timeout for 600s"
    exit

end
